package main

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Grano22/aiculator/internal/meta"
)

func metaCmds() []*cobra.Command {
	show := &cobra.Command{
		Use:   "meta ID",
		Short: "Show a task's properties, files and foreign resources",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			var m meta.Meta
			if err := c.Get(cmd.Context(), "/v1/tasks/"+args[0]+"/meta", &m); err != nil {
				return err
			}
			return printJSON(m)
		},
	}
	prop := &cobra.Command{
		Use:   "prop ID NAME=VALUE...",
		Short: "Set properties; JSON values keep their type, and NAME= removes one",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := meta.Patch{Props: map[string]*meta.Prop{}}
			for _, a := range args[1:] {
				k, v, ok := strings.Cut(a, "=")
				if !ok {
					return fmt.Errorf("%q: use NAME=VALUE", a)
				}
				if v == "" {
					p.Props[k] = nil
					continue
				}
				var val any
				if json.Unmarshal([]byte(v), &val) != nil {
					val = v
				}
				p.Props[k] = &meta.Prop{Value: val}
			}
			c, err := adminClient()
			if err != nil {
				return err
			}
			return c.Post(cmd.Context(), "/v1/tasks/"+args[0]+"/meta", p, nil)
		},
	}
	var attachName string
	attach := &cobra.Command{
		Use:   "attach ID FILE",
		Short: "Store a file as a blob and attach it to a task",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(args[1])
			if err != nil {
				return err
			}
			defer f.Close()
			base := filepath.Base(args[1])
			mediaType := mime.TypeByExtension(filepath.Ext(base))
			if mediaType == "" {
				mediaType = "application/octet-stream"
			}
			c, err := adminClient()
			if err != nil {
				return err
			}
			var ref meta.BlobRef
			if err := c.Upload(cmd.Context(), "/v1/blobs?name="+url.QueryEscape(base), mediaType, f, &ref); err != nil {
				return err
			}
			name := attachName
			if name == "" {
				name = base
			}
			p := meta.Patch{Props: map[string]*meta.Prop{name: {Blob: &ref}}}
			if err := c.Post(cmd.Context(), "/v1/tasks/"+args[0]+"/meta", p, nil); err != nil {
				return err
			}
			fmt.Println(name, ref.SHA256)
			return nil
		},
	}
	attach.Flags().StringVar(&attachName, "name", "", "property name; the file name if empty")
	var kind string
	resource := &cobra.Command{
		Use:   "resource ID NAME URL",
		Short: "Attach a foreign object by reference; its bytes are fetched only when asked for",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			p := meta.Patch{Props: map[string]*meta.Prop{args[1]: {Resource: &meta.Resource{Kind: kind, URL: args[2], Name: args[1]}}}}
			return c.Post(cmd.Context(), "/v1/tasks/"+args[0]+"/meta", p, nil)
		},
	}
	resource.Flags().StringVar(&kind, "kind", "http", "what kind of object this is")
	var output string
	fetch := &cobra.Command{
		Use:   "fetch ID NAME",
		Short: "Save an attached file or foreign resource; a resource is downloaded only once",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			var m meta.Meta
			if err := c.Get(cmd.Context(), "/v1/tasks/"+args[0]+"/meta", &m); err != nil {
				return err
			}
			p, ok := m.Props[args[1]]
			if !ok {
				return fmt.Errorf("%s has no property %s", args[0], args[1])
			}
			var ref meta.BlobRef
			switch {
			case p.Blob != nil:
				ref = *p.Blob
			case p.Resource != nil:
				if err := c.Post(cmd.Context(), "/v1/tasks/"+args[0]+"/resources/"+args[1], nil, &ref); err != nil {
					return err
				}
			default:
				return fmt.Errorf("%s is a plain value: see task meta", args[1])
			}
			body, err := c.Download(cmd.Context(), "/v1/blobs/"+ref.SHA256)
			if err != nil {
				return err
			}
			defer body.Close()
			out := io.Writer(os.Stdout)
			if output != "" {
				f, err := os.Create(output)
				if err != nil {
					return err
				}
				defer f.Close()
				out = f
			}
			_, err = io.Copy(out, body)
			return err
		},
	}
	fetch.Flags().StringVarP(&output, "output", "o", "", "file to write; standard output if empty")
	return []*cobra.Command{show, prop, attach, resource, fetch}
}
