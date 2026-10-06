package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
)

func (c *cli) trust() error {
	if err := c.rejectGlobals("trust: put --%s after trust"); err != nil {
		return err
	}
	fs := flag.NewFlagSet("trust", flag.ContinueOnError)
	repo := fs.String("repo", ".", "project folder to review")
	accept := fs.String("accept", "", "the exact revision shown by a previous review")
	revoke := fs.Bool("revoke", false, "forget this project's trust for future starts and reloads")
	if err := fs.Parse(c.rest()); err != nil {
		return err
	}
	if fs.NArg() != 0 || (*revoke && *accept != "") {
		return fmt.Errorf("trust: use --repo PATH with either --accept REVISION or --revoke")
	}
	root := config.CanonicalPath(*repo)
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return fmt.Errorf("trust: %s is not a folder", root)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if *revoke || *accept != "" {
		if err := config.TrustProject(ctx, c.paths, root, *accept); err != nil {
			return err
		}
		fmt.Println("Project trust saved. Restart existing tasks to discard their loaded code and approvals.")
		return nil
	}
	files, err := config.ReadProjectFiles(root)
	if err != nil {
		return err
	}
	if !files.Present() {
		fmt.Println("This project has no init.scm or allow.toml to trust.")
		return nil
	}
	// Quoting keeps repository-controlled terminal escapes from becoming commands
	// to the terminal while the person reviews the code and its approvals.
	fmt.Printf("Project: %q\n.nodwyn/init.scm: %q\n.nodwyn/allow.toml: %q\nRevision: %s\n", root, files.Init, files.Allow, files.Revision())
	fmt.Println("Trust lets this project's Scheme run with user origin and use its standing approvals. Review code it loads or invokes too. Changes to these two files require another review; other project code is not fingerprinted.")
	quoted := "'" + strings.ReplaceAll(root, "'", "'\"'\"'") + "'"
	fmt.Printf("To accept this revision, run nodwyn trust --repo %s --accept %s\n", quoted, files.Revision())
	return nil
}
