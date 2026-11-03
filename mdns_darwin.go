package main

/*
#include <dns_sd.h>
#include <stdlib.h>

static void nanocRegisterReply(DNSServiceRef sd, DNSRecordRef rr, DNSServiceFlags flags,
	DNSServiceErrorType err, void *ctx) {}

// Registros A (127.0.0.1) e AAAA (::1) só desta máquina (LocalOnly): não vão
// para a rede. Com os dois, quem pede A e AAAA (curl, navegadores) recebe as duas
// respostas na hora; só com o A, o macOS esperava 5 s pelo AAAA.
static DNSServiceErrorType nanocRegister(DNSServiceRef sd, DNSRecordRef *rr, const char *name, int v6) {
	static const unsigned char v4addr[4] = {127, 0, 0, 1};
	static const unsigned char v6addr[16] = {0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1};
	return DNSServiceRegisterRecord(sd, rr, kDNSServiceFlagsUnique, kDNSServiceInterfaceIndexLocalOnly,
		name, v6 ? kDNSServiceType_AAAA : kDNSServiceType_A, kDNSServiceClass_IN,
		v6 ? sizeof(v6addr) : sizeof(v4addr), v6 ? (const void *)v6addr : (const void *)v4addr, 30,
		nanocRegisterReply, NULL);
}
*/
import "C"

import (
	"sync"
	"unsafe"
)

// localNames mantém no mDNSResponder do Mac registros A 127.0.0.1 e AAAA ::1 para
// cada nome *.nanoc.local, visível só nesta máquina e sem precisar de admin (o
// /etc/resolver exigiria). Os registros somem quando a conexão fecha.
type localNames struct {
	mu   sync.Mutex
	sd   C.DNSServiceRef
	recs map[string][2]C.DNSRecordRef
}

func newLocalNames() *localNames {
	return &localNames{recs: map[string][2]C.DNSRecordRef{}}
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
	for n, rrs := range l.recs {
		if !want[n] {
			for _, rr := range rrs {
				if rr != nil {
					C.DNSServiceRemoveRecord(l.sd, rr, 0)
				}
			}
			delete(l.recs, n)
		}
	}
	for n := range want {
		if _, ok := l.recs[n]; ok {
			continue
		}
		cs := C.CString(n + ".")
		var rrs [2]C.DNSRecordRef
		for v6 := range rrs {
			if C.nanocRegister(l.sd, &rrs[v6], cs, C.int(v6)) != C.kDNSServiceErr_NoError {
				rrs[v6] = nil
			}
		}
		l.recs[n] = rrs
		C.free(unsafe.Pointer(cs))
	}
}
