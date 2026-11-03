// Command resolve resolves a dns-sd service instance.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/brutella/dnssd"
	"github.com/brutella/dnssd/cmd/internal/termsafe" // Vault patch: escape untrusted display data
)

var instanceFlag = flag.String("Name", "Service", "Service Name")
var serviceFlag = flag.String("Type", "_asdf._tcp", "Service type")
var domainFlag = flag.String("Domain", "local", "Browsing domain")

var timeFormat = "15:04:05.000"

func main() {
	flag.Parse()
	if len(*instanceFlag) == 0 || len(*serviceFlag) == 0 || len(*domainFlag) == 0 {
		flag.Usage()
		return
	}
	service := fmt.Sprintf("%s.%s.", strings.Trim(*serviceFlag, "."), strings.Trim(*domainFlag, "."))
	instance := fmt.Sprintf("%s.%s.%s.", strings.Trim(*instanceFlag, "."), strings.Trim(*serviceFlag, "."), strings.Trim(*domainFlag, "."))

	fmt.Printf("Lookup %s\n", instance)
	fmt.Printf("DATE: –––%s–––\n", time.Now().Format("Mon Jan 2 2006"))
	fmt.Printf("%s	...STARTING...\n", time.Now().Format(timeFormat))

	addFn := func(e dnssd.BrowseEntry) {
		if e.ServiceInstanceName() == instance {
			text := ""
			for key, value := range e.Text {
				text += fmt.Sprintf("%s=%s", termsafe.String(key), termsafe.String(value)) // Vault patch
			}
			fmt.Printf("%s	%s can be reached at %s %v\n", time.Now().Format(timeFormat), termsafe.String(e.ServiceInstanceName()), e.IPs, text) // Vault patch
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := dnssd.LookupType(ctx, service, addFn, func(dnssd.BrowseEntry) {}); err != nil {
		fmt.Println(err)
		return
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)

	<-stop
	cancel()
}
