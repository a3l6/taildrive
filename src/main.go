package main

import (
	"fmt"
	"net/http"
)

func setupRoutes() {
	http.HandleFunc("/upload", uploadFile)
	http.ListenAndServe(":8080", nil)
}

func main() {
	fmt.Println("Starting server...")
	setupRoutes()
}
