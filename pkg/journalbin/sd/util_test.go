package sd

import (
	"os"
	"strconv"
)

func itoa(n int) string {
	return strconv.Itoa(n)
}

func writeAllFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0644)
}
