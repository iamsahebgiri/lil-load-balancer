package main

import (
	"log"
	"net"
	"time"
)

func main() {
	conn, err := net.DialTimeout("tcp", "google.com:80", 2*time.Second)
	if err != nil {
		log.Fatalf("%s", err)
	}
	defer conn.Close()
	log.Println(conn.RemoteAddr())
}
