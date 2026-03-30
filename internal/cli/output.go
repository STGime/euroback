package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
)

const (
	colorGreen  = "\033[32m"
	colorRed    = "\033[31m"
	colorYellow = "\033[33m"
	colorBold   = "\033[1m"
	colorReset  = "\033[0m"
)

// PrintTable prints an aligned table with headers and rows to stdout.
func PrintTable(headers []string, rows [][]string) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	// Print header
	for i, h := range headers {
		if i > 0 {
			fmt.Fprint(w, "\t")
		}
		fmt.Fprintf(w, "%s%s%s", colorBold, h, colorReset)
	}
	fmt.Fprintln(w)
	// Print rows
	for _, row := range rows {
		for i, col := range row {
			if i > 0 {
				fmt.Fprint(w, "\t")
			}
			fmt.Fprint(w, col)
		}
		fmt.Fprintln(w)
	}
	w.Flush()
}

// PrintJSON prints an indented JSON representation of v to stdout.
func PrintJSON(v interface{}) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "JSON encoding error: %v\n", err)
		return
	}
	fmt.Println(string(data))
}

// PrintSuccess prints a green checkmark with a message.
func PrintSuccess(msg string) {
	fmt.Printf("%s✓%s %s\n", colorGreen, colorReset, msg)
}

// PrintError prints a red X with a message.
func PrintError(msg string) {
	fmt.Fprintf(os.Stderr, "%s✗%s %s\n", colorRed, colorReset, msg)
}

// PrintWarning prints a yellow warning with a message.
func PrintWarning(msg string) {
	fmt.Printf("%s⚠%s %s\n", colorYellow, colorReset, msg)
}
