package main

import (
	"fmt"
	"log"
	"net/http"
)

func greeting(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "Hello, world!")
}

func main() {
	http.HandleFunc("/hello", greeting)
	log.Fatal(http.ListenAndServe(":18080", nil))
}
