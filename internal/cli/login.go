// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/LiStudioorg/licore/hub"
)

// newLoginCommand 实现 `boxli login [HUB]`：向 Hub 换取令牌并缓存到数据目录。
// 令牌绑定到具体 Hub 地址；HUB 省略时取 --hub / $BOXLI_HUB / 默认本地地址。
func newLoginCommand(out io.Writer) *cobra.Command {
	var (
		hubFlag string
		dataDir string
		user    string
		pass    string
	)
	cmd := &cobra.Command{
		Use:   "login [HUB]",
		Short: "登录 Boxli Hub（换取并缓存访问令牌）",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base := hubBaseURL(hubFlag)
			if len(args) == 1 {
				base = args[0]
			}
			if user == "" {
				return fmt.Errorf("login: 需要 --username")
			}
			c := hub.NewClient(base)
			if err := c.Login(user, pass); err != nil {
				return err
			}
			if c.Token == "" {
				return fmt.Errorf("login: 未取得令牌")
			}
			if err := saveHubToken(dataDir, base, c.Token); err != nil {
				return err
			}
			fmt.Fprintf(out, "已登录 %s 用户 %s，令牌已缓存\n", base, user)
			return nil
		},
	}
	cmd.Flags().StringVar(&hubFlag, "hub", "", "Hub 地址（默认 $BOXLI_HUB 或 http://127.0.0.1:3727）")
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "数据目录（默认 $BOXLI_HOME 或 ~/.boxli）")
	cmd.Flags().StringVarP(&user, "username", "u", "", "Hub 用户名（必填）")
	cmd.Flags().StringVarP(&pass, "password", "p", "", "Hub 密码")
	return cmd
}
