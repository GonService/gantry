package extract

import (
	"path/filepath"
	"testing"
)

// Every Portuguese message of the Go code has its English (or is listed as
// not being a message). To fill: go run ./tools/i18ngo
func TestEveryMessageHasEnglish(t *testing.T) {
	root, _ := filepath.Abs("../../..")
	miss, err := Missing(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range miss {
		t.Errorf("sem inglês em internal/i18n/en: %q (%s)", l.Text, l.Pos)
	}
}
