package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// Flags do FSEvents (CoreServices/FSEvents.h), copiadas para a parte sem cgo poder
// ser testada em qualquer build.
const (
	fsMustScanSubDirs = 0x1
	fsItemCreated     = 0x100
	fsItemRemoved     = 0x200
	fsItemInodeMeta   = 0x400
	fsItemRenamed     = 0x800
	fsItemModified    = 0x1000
	fsItemFinderInfo  = 0x2000
	fsItemChangeOwner = 0x4000
	fsItemXattrMod    = 0x8000
	fsOwnEvent        = 0x80000
)

// fileSig é o que decide entre conteúdo mudado (m) e só atributos (a).
type fileSig struct {
	size, mtime int64
	birth       int64
	mode        os.FileMode
	uid, gid    uint32
}

func sigOf(fi os.FileInfo) fileSig {
	s := fileSig{size: fi.Size(), mtime: fi.ModTime().UnixNano(), mode: fi.Mode()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		s.uid, s.gid = st.Uid, st.Gid
		s.birth = st.Birthtimespec.Nano()
	}
	return s
}

// notifyState lembra o último stat de cada caminho avisado. O FSEvents acumula as
// flags de um caminho (um arquivo criado e depois editado chega com Created e
// Modified em todo evento seguinte), então as flags só servem para um caminho
// ainda não visto; depois disso quem decide é a diferença do stat.
type notifyState struct {
	seen  map[string]fileSig
	since int64 // início da observação; Created de arquivo nascido antes é flag velha
}

const notifyStateMax = 100000

// lines traduz um evento do FSEvents nas linhas que o /dev/nanoc_notify do guest
// entende: c (criado), m (modificado), a (atributos) e d (apagado). fi é o lstat de
// agora, nil se o arquivo não existe. Eventos do próprio processo são as escritas
// que o guest fez pelo virtiofs: o kernel do guest já avisou os containers delas.
func (n *notifyState) lines(path string, flags uint32, fi os.FileInfo) []string {
	if flags&fsOwnEvent != 0 || strings.ContainsAny(path, "\n\x00") {
		if fi != nil && flags&fsOwnEvent != 0 {
			n.remember(path, sigOf(fi))
		}
		return nil
	}
	prev, known := n.seen[path]
	if fi == nil {
		delete(n.seen, path)
		if !known && flags&(fsItemCreated|fsItemRemoved|fsItemRenamed|fsItemModified|fsItemInodeMeta) == 0 {
			return nil
		}
		return []string{"d " + path}
	}
	cur := sigOf(fi)
	n.remember(path, cur)
	var out []string
	if !known {
		if flags&(fsItemCreated|fsItemRenamed) != 0 && (cur.birth == 0 || cur.birth >= n.since) {
			out = append(out, "c "+path)
		}
		if flags&fsItemModified != 0 {
			out = append(out, "m "+path)
		}
		if len(out) == 0 && flags&(fsItemInodeMeta|fsItemChangeOwner|fsItemXattrMod|fsItemFinderInfo) != 0 {
			out = append(out, "a "+path)
		}
		return out
	}
	switch {
	case cur.size != prev.size || cur.mtime != prev.mtime:
		return []string{"m " + path}
	case cur != prev:
		return []string{"a " + path}
	}
	return nil
}

func (n *notifyState) remember(path string, s fileSig) {
	if n.seen == nil || len(n.seen) >= notifyStateMax {
		// recomeçar esquece os stats; o próximo evento de cada caminho volta às flags
		n.seen = map[string]fileSig{}
	}
	n.seen[path] = s
}

// bindRoots lê a resposta de GET /containers/json e devolve as pastas do Mac que os
// containers montam, sem as que já estão dentro de outra. Montagem de um arquivo
// vira a pasta dele, porque o FSEvents observa pastas.
func bindRoots(body []byte, under string, isDir func(string) bool) []string {
	var cs []struct {
		Mounts []struct {
			Type   string
			Source string
		}
	}
	if json.Unmarshal(body, &cs) != nil {
		return nil
	}
	set := map[string]bool{}
	for _, c := range cs {
		for _, m := range c.Mounts {
			p := filepath.Clean(m.Source)
			if m.Type != "bind" || !strings.HasPrefix(p+"/", under+"/") {
				continue
			}
			if !isDir(p) {
				p = filepath.Dir(p)
			}
			set[p] = true
		}
	}
	var all []string
	for p := range set {
		all = append(all, p)
	}
	sort.Strings(all)
	var roots []string
	for _, p := range all {
		if n := len(roots); n > 0 && strings.HasPrefix(p+"/", roots[n-1]+"/") {
			continue
		}
		roots = append(roots, p)
	}
	return roots
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// notifyBatches parte as linhas em escritas que cabem no limite do módulo
// (NOTIFY_MAX_LINE * 16 bytes por write).
func notifyBatches(lines []string, max int) [][]string {
	var out [][]string
	var cur []string
	size := 0
	for _, l := range lines {
		if len(cur) > 0 && size+len(l)+1 > max {
			out = append(out, cur)
			cur, size = nil, 0
		}
		cur = append(cur, l)
		size += len(l) + 1
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}
