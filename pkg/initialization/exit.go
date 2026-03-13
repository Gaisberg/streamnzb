package initialization

import (
	"fmt"
	"os"

	"streamnzb/pkg/core/logger"
)

func WaitForInputAndExit(err error) {
	logger.Error("CRITICAL ERROR", "err", err)
	fmt.Println("\nPress Enter to exit...")
	var input string
	fmt.Scanln(&input)
	os.Exit(1)
}