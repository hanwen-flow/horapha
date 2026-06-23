package bazel

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func asExit(err error, target **exec.ExitError) bool {
	return errors.As(err, target)
}

func readPIDFile(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}
