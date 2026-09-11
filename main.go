package main

import (
	"context"
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		fmt.Println("GITHUB_TOKEN env var is required for GraphQL API")
		os.Exit(1)
	}
	client := NewRealGHClient(token)
	Run(context.Background(), client)
}
