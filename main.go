package main

import (
	"fmt"
	"os/user"

	"github.com/CloudNativeWorks/elchi-client/cmd"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
)

var version = "1.0.0"

func main() {

	u, err := user.Current()
	if err != nil {
		fmt.Println("User info not found:", err)
		return
	}
	fmt.Println("Running user:", u.Username)

	if err := cmd.Execute(version); err != nil {
		logger.Fatalf("Error: %v", err)
	}
}
