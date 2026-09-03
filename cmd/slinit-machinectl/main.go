// slinit-machinectl — inspect + tweak slinit's flat-file container
// registry (/run/slinit/machines/). Modelled on systemd machinectl's
// most useful subset for slinit's simpler runtime model:
//
//   register <name> <pid> [--class=…] [--service=…] [--root=…]
//       Write a registry entry. Overwrites atomically.
//   unregister <name>
//       Delete a registry entry.
//   list
//       Table of every registered machine + Alive status.
//   status <name>
//       Verbose dump of one machine's registry fields + liveness.
//   show <name>
//       Raw contents of the registry file (for scripting/debug).
//
// No D-Bus, no machined daemon. slinit-nspawn (Tier 3) and any
// operator scripts write registry entries directly via pkg/machine;
// this CLI is just a convenient facade over the same package.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"

	"github.com/sunlightlinux/slinit/pkg/machine"
)

const usage = `Usage: slinit-machinectl <command> [args…]

Commands:
  register <name> <pid> [--class=CLASS] [--service=SVC] [--root=PATH]
                                 Register a container in /run/slinit/machines/
  unregister <name>              Remove a machine's registry entry
  list                           List every registered machine + liveness
  status <name>                  Show one machine's fields + liveness
  show <name>                    Print the raw registry file for scripting

Env:
  SLINIT_MACHINES_DIR            Override the registry directory (test/rootless)

Registry format is a plain file per machine — inspect with cat(1),
scripts can write directly if they prefer not to use this tool.
`

func main() {
	if dir := os.Getenv("SLINIT_MACHINES_DIR"); dir != "" {
		machine.SetDir(dir)
	}
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "-h", "--help", "help":
		fmt.Print(usage)
	case "register":
		err = doRegister(args)
	case "unregister":
		err = doUnregister(args)
	case "list":
		err = doList(args)
	case "status":
		err = doStatus(args)
	case "show":
		err = doShow(args)
	default:
		fmt.Fprintf(os.Stderr, "slinit-machinectl: unknown command %q (try -h)\n", cmd)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit-machinectl: %v\n", err)
		os.Exit(1)
	}
}

func doRegister(args []string) error {
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	var class, service, root string
	fs.StringVar(&class, "class", "", "informational class tag (container/vm/namespace/chroot)")
	fs.StringVar(&service, "service", "", "slinit service that owns this container")
	fs.StringVar(&root, "root", "", "container rootfs as visible from the host (else /proc/PID/root)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("register: want <name> <pid>, got %d args", fs.NArg())
	}
	name := fs.Arg(0)
	pid, err := strconv.Atoi(fs.Arg(1))
	if err != nil {
		return fmt.Errorf("register: pid %q: %w", fs.Arg(1), err)
	}
	return machine.Register(machine.Machine{
		Name: name, PID: pid, Class: class, Service: service, Root: root,
	})
}

func doUnregister(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("unregister: want <name>, got %d args", len(args))
	}
	return machine.Unregister(args[0])
}

func doList(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("list: takes no arguments")
	}
	machines, err := machine.List()
	if err != nil {
		return err
	}
	if len(machines) == 0 {
		fmt.Println("(no machines registered)")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPID\tALIVE\tCLASS\tSERVICE\tROOT")
	for _, m := range machines {
		alive := "no"
		if machine.Alive(m.PID) {
			alive = "yes"
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\n",
			m.Name, m.PID, alive, dashIfEmpty(m.Class),
			dashIfEmpty(m.Service), dashIfEmpty(m.Root))
	}
	return w.Flush()
}

func doStatus(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("status: want <name>, got %d args", len(args))
	}
	m, err := machine.Lookup(args[0])
	if err != nil {
		return err
	}
	if m == nil {
		return fmt.Errorf("status: no such machine %q under %s", args[0], machine.Dir())
	}
	fmt.Printf("Name:    %s\n", m.Name)
	fmt.Printf("PID:     %d\n", m.PID)
	fmt.Printf("Alive:   %v\n", machine.Alive(m.PID))
	fmt.Printf("Class:   %s\n", dashIfEmpty(m.Class))
	fmt.Printf("Service: %s\n", dashIfEmpty(m.Service))
	fmt.Printf("Root:    %s\n", dashIfEmpty(m.Root))
	if m.Root == "" {
		fmt.Printf("         (resolves to /proc/%d/root)\n", m.PID)
	}
	return nil
}

func doShow(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("show: want <name>, got %d args", len(args))
	}
	m, err := machine.Lookup(args[0])
	if err != nil {
		return err
	}
	if m == nil {
		return fmt.Errorf("show: no such machine %q", args[0])
	}
	fmt.Printf("%d\n", m.PID)
	if m.Class != "" {
		fmt.Printf("CLASS=%s\n", m.Class)
	}
	if m.Service != "" {
		fmt.Printf("SERVICE=%s\n", m.Service)
	}
	if m.Root != "" {
		fmt.Printf("ROOT=%s\n", m.Root)
	}
	return nil
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
