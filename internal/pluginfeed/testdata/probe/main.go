package main

// 本文件只做一件事：把内置订阅地址打印出来。
//
// 它放在 testdata 下（不会参与 go build ./...），供 builtin_test.go 用真实的
// -ldflags 构建并运行 —— 这样能验证「构建时覆盖」真的生效，而不是测一个我们以为
// 存在的机制。

import (
	"fmt"

	"github.com/dezhishen/upkit/internal/pluginfeed"
)

func main() {
	fmt.Print(pluginfeed.BuiltinFeedURL)
}
