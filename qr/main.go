// Command qr draws a QR code in the terminal, and optionally as a PNG for a
// slide, so a room full of people can subscribe to the ntfy topic by
// scanning instead of typing. It runs from the prebuilt image (make
// subscribe), so it needs no compiler and no network on the day.
//
//	qr <url> [out.png]
package main

import (
	"fmt"
	"os"

	qrcode "github.com/skip2/go-qrcode"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: qr <url> [out.png]")
		os.Exit(2)
	}
	q, err := qrcode.New(os.Args[1], qrcode.Medium)
	if err != nil {
		fmt.Fprintln(os.Stderr, "qr:", err)
		os.Exit(1)
	}
	// Dark terminal, light modules: invert so phones read it off a projector.
	fmt.Print(q.ToSmallString(true))
	if len(os.Args) > 2 {
		if err := q.WriteFile(1024, os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "qr:", err)
			os.Exit(1)
		}
	}
}
