// Copyright 2026 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package vec

import (
	"modernc.org/libc"
)

func ___inline_isnanf(tls *libc.TLS, f float32) int32 {
	return libc.X__inline_isnanf(tls, f)
}

func ___inline_isnan(tls *libc.TLS, f float64) int32 {
	return libc.X__inline_isnand(tls, f)
}
