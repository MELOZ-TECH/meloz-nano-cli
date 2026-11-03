package main

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeFI struct {
	os.FileInfo
	size int64
	mode os.FileMode
	mt   time.Time
}

func (f fakeFI) Size() int64        { return f.size }
func (f fakeFI) Mode() os.FileMode  { return f.mode }
func (f fakeFI) ModTime() time.Time { return f.mt }
func (f fakeFI) Sys() any           { return nil }

func TestNotifyLines(t *testing.T) {
	p := "/Users/a/f"
	t0 := time.Unix(100, 0)
	v1 := fakeFI{size: 3, mode: 0o644, mt: t0}
	v2 := fakeFI{size: 9, mode: 0o644, mt: t0.Add(time.Second)}
	v3 := fakeFI{size: 9, mode: 0o600, mt: t0.Add(time.Second)}
	var n notifyState
	steps := []struct {
		flags uint32
		fi    os.FileInfo
		want  []string
	}{
		{fsItemCreated | fsItemModified, v1, []string{"c " + p, "m " + p}},
		// flags acumuladas: Created continua vindo, quem manda é o stat
		{fsItemCreated | fsItemModified, v2, []string{"m " + p}},
		{fsItemCreated | fsItemModified | fsItemInodeMeta, v3, []string{"a " + p}},
		{fsItemCreated | fsItemModified | fsItemInodeMeta, v3, nil},
		{fsItemModified | fsOwnEvent, v1, nil},
		{fsItemModified, v1, nil},
		{fsItemRemoved, nil, []string{"d " + p}},
		{fsMustScanSubDirs, nil, nil},
		{fsItemRenamed, v1, []string{"c " + p}},
		{fsItemRenamed, nil, []string{"d " + p}},
	}
	for i, s := range steps {
		var fi os.FileInfo
		if s.fi != nil {
			fi = s.fi
		}
		if got := n.lines(p, s.flags, fi); !reflect.DeepEqual(got, s.want) {
			t.Errorf("passo %d flags %#x: %q, quero %q", i, s.flags, got, s.want)
		}
	}
	var m notifyState
	if got := m.lines(p, fsItemInodeMeta, v1); !reflect.DeepEqual(got, []string{"a " + p}) {
		t.Errorf("atributo de arquivo ainda não visto: %q", got)
	}
	if got := m.lines("/Users/a/x\ny", fsItemModified, v1); got != nil {
		t.Errorf("nome com quebra de linha: %q", got)
	}
}

func TestBindRoots(t *testing.T) {
	body := []byte(`[
	 {"Mounts":[{"Type":"bind","Source":"/Users/r/app"},{"Type":"volume","Source":"/var/lib/docker/v"}]},
	 {"Mounts":[{"Type":"bind","Source":"/Users/r/app/src"},{"Type":"bind","Source":"/Users/r/cfg/x.yml"}]},
	 {"Mounts":[{"Type":"bind","Source":"/Users/rr"},{"Type":"bind","Source":"/var/run/docker.sock"}]}]`)
	dir := func(p string) bool { return !strings.HasSuffix(p, ".yml") }
	got := bindRoots(body, "/Users", dir)
	want := []string{"/Users/r/app", "/Users/r/cfg", "/Users/rr"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%q, quero %q", got, want)
	}
}

func TestNotifyBatches(t *testing.T) {
	got := notifyBatches([]string{"m /a", "m /b", "m /c"}, 10)
	want := [][]string{{"m /a", "m /b"}, {"m /c"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%q, quero %q", got, want)
	}
}
