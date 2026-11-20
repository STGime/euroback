package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// A big import writes hundreds of files; the list is shortened so the
// "Read before applying" notes printed after it stay on screen.
func TestPrintWrittenFiles(t *testing.T) {
	var few, many bytes.Buffer
	printWrittenFiles(&few, []string{"m/1_part_001.sql", "m/1_part_002.sql"})
	if strings.Count(few.String(), "Migration written to") != 2 {
		t.Errorf("short list should name every file:\n%s", few.String())
	}

	var paths []string
	for i := 1; i <= 104; i++ {
		paths = append(paths, fmt.Sprintf("m/1_part_%03d.sql", i))
	}
	printWrittenFiles(&many, paths)
	out := many.String()
	for _, want := range []string{"104 migration files written to m:", "1_part_001.sql", "1_part_104.sql", "… 98 more …"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "1_part_050.sql") {
		t.Errorf("middle files should be left out:\n%s", out)
	}
}

func TestPrintReadBeforeApplying(t *testing.T) {
	var none, some bytes.Buffer
	printReadBeforeApplying(&none, nil)
	if none.Len() != 0 {
		t.Errorf("no notes should print nothing, got %q", none.String())
	}
	printReadBeforeApplying(&some, []string{"60 users have no password"})
	if !strings.Contains(some.String(), "Read before applying") || !strings.Contains(some.String(), "60 users have no password") {
		t.Errorf("unexpected output: %q", some.String())
	}
}
