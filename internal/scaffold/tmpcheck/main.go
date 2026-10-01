package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/LiStudioorg/boxli/internal/build"
	"github.com/LiStudioorg/boxli/internal/compose"
	"github.com/LiStudioorg/boxli/internal/scaffold"
)

func main() {
	dir, _ := os.MkdirTemp("", "roundtrip-")
	defer os.RemoveAll(dir)
	if _, err := scaffold.Init(dir, false); err != nil {
		panic(err)
	}

	// 1. 生成的 compose 必须能被 internal/compose.ParseProject 解析
	cd, err := os.ReadFile(filepath.Join(dir, "boxli-compose.yml"))
	fmt.Printf("compose read err=%v\n", err)
	p, err := compose.ParseProject(cd)
	fmt.Printf("compose.ParseProject err=%v\n", err)
	if err == nil {
		fmt.Printf("  Version=%q Name=%q Dir=%q services=%d\n", p.Version, p.Name, p.Dir, len(p.Services))
		for k, s := range p.Services {
			fmt.Printf("  svc %q: Boxfile=%q Image=%q Restart=%q Ports=%v Volumes=%v Env=%v Dev=%+v\n",
				k, s.Boxfile, s.Image, s.Restart, s.Ports, s.Volumes, s.Environment, s.Dev)
		}
		fmt.Printf("  Validate err=%v\n", p.Validate())
	}

	// 2. 生成的 Boxfile 必须能被 internal/build.ParseBoxfile 解析
	bd, _ := os.ReadFile(filepath.Join(dir, "Boxfile"))
	bf, err := build.ParseBoxfile(bd)
	fmt.Printf("build.ParseBoxfile err=%v\n", err)
	if err == nil {
		fmt.Printf("  From=%q instructions=%d\n", bf.From, len(bf.Instructions))
		for _, in := range bf.Instructions {
			fmt.Printf("    L%d %s %v\n", in.Line, in.Op, in.Args)
		}
	}

	// 3. LoadFile 路径
	p2, err := compose.LoadFile(filepath.Join(dir, "boxli-compose.yml"))
	fmt.Printf("compose.LoadFile err=%v name=%q\n", err, func() string { if p2 == nil { return "" }; return p2.Name }())
}
