//go:build linux

// vsock-proxy: escuta em AF_VSOCK:2375 no guest e encaminha para o docker.sock.
package main

import (
	"io"
	"log"
	"net"
)

const (
	vsockPort  = 2375
	dockerSock = "/var/run/docker.sock"
)

func main() {
	ln, err := net.Listen("tcp", ":2375")
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	log.Printf("tcp-proxy ouvindo em :2375 -> %s", dockerSock)
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		go handle(conn)
	}
}

func handle(conn net.Conn) {
	defer conn.Close()

	up, err := net.Dial("unix", dockerSock)
	if err != nil {
		log.Printf("dial docker.sock: %v", err)
		return
	}
	defer up.Close()

	go io.Copy(up, conn)
	io.Copy(conn, up)
}
