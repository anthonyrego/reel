package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/anthonyrego/reel/internal/mac"
)

func init() {
	register(&command{name: "devices", summary: "list the permissions, displays, windows, microphones and cameras (opens nothing, asks nothing)", run: runDevices})
}

func runDevices(args []string) error {
	fs := flag.NewFlagSet("devices", flag.ContinueOnError)
	allWindows := fs.Bool("all", false, "list off-screen windows too")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	defer tw.Flush()

	screen, micAccess, camAccess := mac.ScreenAccess(), mac.MicAccess(), mac.CameraAccess()
	fmt.Fprintf(tw, "permissions\n")
	fmt.Fprintf(tw, "  screen recording\t%s\n", screen)
	fmt.Fprintf(tw, "  microphone\t%s\n", micAccess)
	fmt.Fprintf(tw, "  camera\t%s\n", camAccess)

	fmt.Fprintf(tw, "\nmicrophones\n")
	mics, err := mac.Mics()
	if err != nil {
		return err
	}
	for _, m := range mics {
		def := ""
		if m.Default {
			def = "default"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", m.Name, m.Transport, def, m.ID)
	}

	// iPhones are listed only in Reel.app (its Info.plist asks for them).
	fmt.Fprintf(tw, "\ncameras\n")
	cams, err := mac.Cameras()
	if err != nil {
		return err
	}
	for _, c := range cams {
		def := ""
		if c.Default {
			def = "default"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", c.Name, c.Kind, def, c.ID)
	}

	if screen != mac.PermissionGranted {
		fmt.Fprintf(tw, "\ndisplays and windows need Screen Recording\n")
		return nil
	}
	content, err := mac.ShareableContent()
	if err != nil {
		return err
	}
	fmt.Fprintf(tw, "\ndisplays\n")
	for _, d := range content.Displays {
		fmt.Fprintf(tw, "  %d\t%.0f×%.0f pt at %.0f,%.0f\t%.0fx\n", d.ID, d.Frame.W, d.Frame.H, d.Frame.X, d.Frame.Y, d.Scale)
	}
	fmt.Fprintf(tw, "\nwindows\n")
	for _, w := range content.Windows {
		if !w.OnScreen && !*allWindows {
			continue
		}
		fmt.Fprintf(tw, "  %d\t%s\t%q\t%.0f×%.0f pt\n", w.ID, w.App, w.Title, w.Frame.W, w.Frame.H)
	}
	return nil
}
