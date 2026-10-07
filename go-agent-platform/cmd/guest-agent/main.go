package main

import (
	"flag"
	"log"
)

func main() {
	gateway := flag.String("gateway", "http://127.0.0.1:8081", "gateway base URL")
	flag.Parse()
	log.Printf("guest-agent is ready; gateway=%s", *gateway)
}
