package main

/*
#include <dns_sd.h>
#include <stdlib.h>

static void nanocRegisterReply(DNSServiceRef sd, DNSRecordRef rr, DNSServiceFlags flags,
	DNSServiceErrorType err, void *ctx) {}

// Registro A só desta máquina (LocalOnly): não vai para a rede.
static DNSServiceErrorType nanocRegisterA(DNSServiceRef sd, DNSRecordRef *rr, const char *name) {
	static const unsigned char loopback[4] = {127, 0, 0, 1};
	return DNSServiceRegisterRecord(sd, rr, kDNSServiceFlagsShared, kDNSServiceInterfaceIndexLocalOnly,
		name, kDNSServiceType_A, kDNSServiceClass_IN, sizeof(loopback), loopback, 30,
		nanocRegisterReply, NULL);
}
*/
import "C"

import (
	"sync"
	"unsafe"
)

// localNames mantém no mDNSResponder do Mac um registro A 127.0.0.1 para cada
// nome *.nanoc.local, visível só nesta máquina e sem precisar de admin (o
// /etc/resolver exigiria). Os registros somem quando a conexão fecha.
type localNames struct {
	mu   sync.Mutex
	sd   C.DNSServiceRef
	recs map[string]C.DNSRecordRef
}

func newLocalNames() *localNames {
	return &localNames{recs: map[string]C.DNSRecordRef{}}
}

func (l *localNames) set(names []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sd == nil {
		if C.DNSServiceCreateConnection(&l.sd) != C.kDNSServiceErr_NoError {
			l.sd = nil
			return
		}
		sd := l.sd
		// as respostas do mDNSResponder precisam ser lidas, senão o socket enche
		go func() {
			for C.DNSServiceProcessResult(sd) == C.kDNSServiceErr_NoError {
			}
		}()
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	for n, rr := range l.recs {
		if !want[n] {
			C.DNSServiceRemoveRecord(l.sd, rr, 0)
			delete(l.recs, n)
		}
	}
	for n := range want {
		if _, ok := l.recs[n]; ok {
			continue
		}
		cs := C.CString(n + ".")
		var rr C.DNSRecordRef
		if C.nanocRegisterA(l.sd, &rr, cs) == C.kDNSServiceErr_NoError {
			l.recs[n] = rr
		}
		C.free(unsafe.Pointer(cs))
	}
}
