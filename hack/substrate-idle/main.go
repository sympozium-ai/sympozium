// Command substrate-idle parks actors so their idle cost can be measured
// from outside (spike 0b, #699, checkpoint 7). Each step acts on actors
// idle-<from>..idle-<to-1>: "resume" creates them if needed and resumes them
// (the runner then sits in its polling loop, with no egress at all),
// "suspend" snapshots them to storage and frees the worker, "delete" removes
// them. It is not shipped.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/sympozium-ai/sympozium/internal/substrate"
)

func main() {
	step := flag.String("step", "", "resume | suspend | delete | list")
	from := flag.Int("from", 0, "first actor index")
	to := flag.Int("to", 0, "one past the last actor index")
	atespace := flag.String("atespace", "symp-spike", "atespace")
	template := flag.String("template", "", "actor template (must already have a golden snapshot)")
	flag.Parse()

	settings, err := substrate.SettingsFromEnv(os.Getenv)
	must(err)
	if settings == nil {
		must(fmt.Errorf("SUBSTRATE_* settings are required"))
	}
	c, err := settings.Dial()
	must(err)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if *step == "list" {
		actors, err := c.ListActors(ctx, *atespace)
		must(err)
		count := map[substrate.State]int{}
		for _, a := range actors {
			count[a.State]++
		}
		fmt.Println(count)
		return
	}
	for i := *from; i < *to; i++ {
		name := fmt.Sprintf("idle-%03d", i)
		start := time.Now()
		switch *step {
		case "resume":
			_, _, err := c.CreateActor(ctx, *atespace, name, *template)
			must(err)
			for {
				_, err = c.ResumeActor(ctx, *atespace, name)
				if err == nil || !substrate.IsUnavailable(err) {
					break
				}
				time.Sleep(500 * time.Millisecond)
			}
			must(err)
		case "suspend":
			_, err := c.SuspendActor(ctx, *atespace, name, true)
			must(err)
		case "delete":
			for {
				gone, err := c.DeleteActor(ctx, *atespace, name)
				must(err)
				if gone {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
		default:
			must(fmt.Errorf("unknown step %q", *step))
		}
		fmt.Printf("%s %s %.0fms\n", *step, name, float64(time.Since(start).Microseconds())/1000)
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "substrate-idle:", err)
		os.Exit(1)
	}
}
