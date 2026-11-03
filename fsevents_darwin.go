//go:build krun

package main

/*
#cgo LDFLAGS: -framework CoreServices
#include <CoreServices/CoreServices.h>
#include <dispatch/dispatch.h>
#include <stdint.h>
#include <stdlib.h>

extern void goFSEvents(uintptr_t h, size_t n, char **paths, FSEventStreamEventFlags *flags);

static void fsevCallback(ConstFSEventStreamRef s, void *info, size_t n, void *paths,
	const FSEventStreamEventFlags flags[], const FSEventStreamEventId ids[]) {
	goFSEvents((uintptr_t)info, n, (char **)paths, (FSEventStreamEventFlags *)flags);
}

// MarkSelf marca com OwnEvent o que este processo escreveu, ou seja, o que o guest
// escreveu pelo virtiofs.
static FSEventStreamRef fsevStart(uintptr_t h, char **paths, int n, double latency) {
	CFMutableArrayRef arr = CFArrayCreateMutable(NULL, n, &kCFTypeArrayCallBacks);
	for (int i = 0; i < n; i++) {
		CFStringRef s = CFStringCreateWithCString(NULL, paths[i], kCFStringEncodingUTF8);
		CFArrayAppendValue(arr, s);
		CFRelease(s);
	}
	FSEventStreamContext ctx = {0, (void *)h, NULL, NULL, NULL};
	FSEventStreamRef st = FSEventStreamCreate(NULL, fsevCallback, &ctx, arr,
		kFSEventStreamEventIdSinceNow, latency,
		kFSEventStreamCreateFlagFileEvents | kFSEventStreamCreateFlagNoDefer |
		kFSEventStreamCreateFlagMarkSelf);
	CFRelease(arr);
	if (!st)
		return NULL;
	FSEventStreamSetDispatchQueue(st, dispatch_queue_create("nanoc.fsevents", NULL));
	if (!FSEventStreamStart(st)) {
		FSEventStreamInvalidate(st);
		FSEventStreamRelease(st);
		return NULL;
	}
	return st;
}

static void fsevStop(FSEventStreamRef st) {
	FSEventStreamStop(st);
	FSEventStreamInvalidate(st);
	FSEventStreamRelease(st);
}
*/
import "C"

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime/cgo"
	"time"
	"unsafe"
)

type fsEvent struct {
	path  string
	flags uint32
}

//export goFSEvents
func goFSEvents(h C.uintptr_t, n C.size_t, paths **C.char, flags *C.FSEventStreamEventFlags) {
	ch := cgo.Handle(h).Value().(chan fsEvent)
	ps := unsafe.Slice(paths, int(n))
	fs := unsafe.Slice(flags, int(n))
	for i := range ps {
		select {
		case ch <- fsEvent{C.GoString(ps[i]), uint32(fs[i])}:
		default: // fila cheia: perder um aviso só atrasa até o fim do cache curto
		}
	}
}

type fsStream struct {
	ref C.FSEventStreamRef
	h   cgo.Handle
}

func startFSEvents(paths []string, ch chan fsEvent) (*fsStream, error) {
	cs := make([]*C.char, len(paths))
	for i, p := range paths {
		cs[i] = C.CString(p)
		defer C.free(unsafe.Pointer(cs[i]))
	}
	h := cgo.NewHandle(ch)
	ref := C.fsevStart(C.uintptr_t(h), &cs[0], C.int(len(cs)), 0.03)
	if ref == nil {
		h.Delete()
		return nil, fmt.Errorf("FSEventStreamStart falhou")
	}
	return &fsStream{ref, h}, nil
}

func (s *fsStream) stop() {
	C.fsevStop(s.ref)
	s.h.Delete()
}

// krunNotify leva ao guest as mudanças que o Mac faz nas pastas que os containers
// montam, como o OrbStack faz com o FSEvents e o /dev/krpc: o kernel do guest
// descarta o cache daquele arquivo e os containers recebem o evento do inotify, o
// que faz o hot reload funcionar sem polling.
func krunNotify(agentControl, dockerSock, ev string) {
	ko := envOr("NANOC_KRUN_NOTIFY_KO", filepath.Join(os.Getenv("HOME"), ".nanoc", "nanoc_notify.ko"))
	if _, err := os.Stat(ko); err != nil || os.Getenv("NANOC_KRUN_NOTIFY") == "0" {
		return
	}
	// o guest vê o .ko pelo compartilhamento de /Users, no mesmo caminho
	r := krunAgent(agentControl, map[string]any{"cmd": "run", "argv": []string{"sh", "-c",
		"[ -e /dev/nanoc_notify ] || insmod \"$1\"; [ -e /dev/nanoc_notify ]", "_", ko}})
	if r["ok"] != true {
		logEvent(ev, "notify_off", map[string]any{"error": fmt.Sprint(r["message"], r["output"])})
		return
	}
	logEvent(ev, "notify_ready", map[string]any{"ko": ko})

	ch := make(chan fsEvent, 4096)
	go notifySender(agentControl, ch)
	cl := dockerClient(dockerSock)
	var cur []string
	var st *fsStream
	for {
		if resp, err := cl.Get("http://docker/containers/json"); err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			roots := bindRoots(b, "/Users", isDir)
			if !reflect.DeepEqual(roots, cur) {
				if st != nil {
					st.stop()
					st = nil
				}
				if len(roots) > 0 {
					if st, err = startFSEvents(roots, ch); err != nil {
						logEvent(ev, "notify_watch_error", map[string]any{"error": err.Error()})
					}
				}
				cur = roots
				logEvent(ev, "notify_watch", map[string]any{"paths": roots})
			}
		}
		time.Sleep(2 * time.Second)
	}
}

// notifySender junta os eventos de alguns milissegundos e manda tudo num processo
// só do guest. Cada linha vai num write próprio: o buffer do stdio do musl tem 1 KiB
// e partiria linhas longas entre dois writes.
func notifySender(agentControl string, ch chan fsEvent) {
	st := notifyState{since: time.Now().UnixNano()}
	for e := range ch {
		seen := map[string]bool{}
		var lines []string
		add := func(e fsEvent) {
			var fi os.FileInfo
			if v, err := os.Lstat(e.path); err == nil {
				fi = v
			}
			for _, l := range st.lines(e.path, e.flags, fi) {
				if !seen[l] {
					seen[l] = true
					lines = append(lines, l)
				}
			}
		}
		add(e)
		deadline := time.After(20 * time.Millisecond)
	more:
		for {
			select {
			case e := <-ch:
				add(e)
			case <-deadline:
				break more
			}
		}
		for _, b := range notifyBatches(lines, 60000) {
			krunAgent(agentControl, map[string]any{"cmd": "run", "argv": append([]string{"sh", "-c",
				`for l; do printf '%s\n' "$l" > /dev/nanoc_notify; done`, "_"}, b...)})
		}
	}
}
