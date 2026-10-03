// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package compose

import "errors"

// 本包定义的所有哨兵错误。调用方必须用 errors.Is 判定，禁止匹配错误字符串。
var (
	// ErrYAML 表示输入的 YAML 不是本包支持的子集，或存在语法错误。
	// 解析器只做严格解析，禁止"尽力猜测"式宽容。
	ErrYAML = errors.New("licore/compose: YAML 非法")

	// ErrUnsupported 表示输入使用了本包明确不支持的 YAML 特性，
	// 例如锚点/别名、标签、多文档、流式集合、块标量。
	ErrUnsupported = errors.New("licore/compose: 不支持的 YAML 特性")

	// ErrBadProject 表示 compose 文件结构非法：未知键、类型不符、必填字段缺失或取值非法。
	ErrBadProject = errors.New("licore/compose: compose 文件非法")

	// ErrBadService 表示单个服务的某个字段非法。
	// 与 ErrBadProject 语义一致（项目级检查同样把服务错误视为项目非法），
	// 故别名同一哨兵，保证 errors.Is 对两者都成立。
	ErrBadService = ErrBadProject

	// ErrCycle 表示 depends_on 依赖图存在环，无法确定启动顺序。
	ErrCycle = errors.New("licore/compose: 服务依赖成环")
)
