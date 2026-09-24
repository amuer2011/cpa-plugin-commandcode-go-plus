package plugin

import (
	"errors"
	"os"
)

var rename = os.Rename
var readFile = os.ReadFile
var removeFile = os.Remove

func isNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }
