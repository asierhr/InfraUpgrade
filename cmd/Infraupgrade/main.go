package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/asierhr/infraupgrade/internal/scanner"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "scan":
		path := "."
		if len(os.Args) > 3 {
			fmt.Fprintln(os.Stderr, "scan accepts at most one path")
			printUsage()
			os.Exit(1)
		}
		if len(os.Args) == 3 {
			path = os.Args[2]
		}

		result, err := scanner.Scan(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "scan failed: %v\n", err)
			os.Exit(1)
		}
		printScanResult(result)
	case "version":
		fmt.Println("infraupgrade development")
	default:
		fmt.Printf("Unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage:")
	fmt.Println("  infraupgrade scan [path]")
	fmt.Println("  infraupgrade version")
}

func printScanResult(result scanner.Result) {
	fmt.Printf("Project: %s\n", filepath.Clean(result.Root))
	fmt.Printf("Terraform files: %d\n", len(result.TerraformFiles))

	if result.RequiredVersion == "" {
		fmt.Println("Terraform version: not specified")
	} else {
		fmt.Printf("Terraform version: %s\n", result.RequiredVersion)
	}

	if len(result.Providers) == 0 {
		fmt.Println("Providers: none found")
		return
	}

	fmt.Println("Providers:")
	for _, provider := range result.Providers {
		fmt.Printf("  - %s", provider.Name)
		if provider.Source != "" {
			fmt.Printf(" (%s)", provider.Source)
		}
		fmt.Println()

		if provider.Constraint != "" {
			fmt.Printf("    Constraint: %s\n", provider.Constraint)
		} else {
			fmt.Println("    Constraint: not specified")
		}

		if provider.LockedVersion != "" {
			fmt.Printf("    Locked: %s\n", provider.LockedVersion)
		} else {
			fmt.Println("    Locked: not found")
		}

		fmt.Printf(
			"    Declared: %t\n",
			provider.Declared,
		)

		fmt.Printf(
			"    Configured: %t\n",
			provider.Configured,
		)

		fmt.Printf(
			"    Inferred: %t\n",
			provider.Inferred,
		)

		fmt.Printf(
			"    Resources: %d\n",
			provider.ResourceCount,
		)
	}
}
