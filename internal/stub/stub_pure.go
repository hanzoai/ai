//go:build !arm64 && (!amd64 || !amd64.v2)

package stub

import (
	_ "embed"
	"unsafe"
)

func fn7(m *Module, l0 int32, l1 int32, l2 int32, l3 int32, l4 int32) {
	mBase := m.M
	_ = mBase
	var v8 int32
	_ = v8
	var v10 int32
	_ = v10
	var v13 int32
	_ = v13
	var v14 int32
	_ = v14
	var v15 int32
	_ = v15
	var v23 int32
	_ = v23
	var v24 int32
	_ = v24
	v8 = m.g0
	v10 = v8 - int32(16)
	m.g0 = v10
	v13 = v10 + int32(8)
	v14 = *(*int32)(unsafe.Add(mBase, uint32(l2)))
	v15 = v14 + l4
	if ui32(v15) < ui32(v14) {
		fn15(m, v14, v15, l1)
		mBase = m.M
		wasm_trap_unreachable()
		for {
		}
	} else {
		if ui32(l1) < ui32(v15) {
			fn15(m, v14, v15, l1)
			mBase = m.M
			wasm_trap_unreachable()
			for {
			}
		} else {
			*(*int32)(unsafe.Add(mBase, uint32(v13)+4)) = v15 - v14
			*(*int32)(unsafe.Add(mBase, uint32(v13))) = l0 + v14
			v23 = *(*int32)(unsafe.Add(mBase, uint32(v10)+8))
			v24 = *(*int32)(unsafe.Add(mBase, uint32(v10)+12))
			if v24 != l4 {
				fn14(m, v24, l4)
				mBase = m.M
				wasm_trap_unreachable()
				for {
				}
			} else {
				if v24 == int32(0) {
				} else {
					memoryCopy(m, v23, l3, v24)
				}
				*(*int32)(unsafe.Add(mBase, uint32(l2))) = v15
				m.g0 = v10 + int32(16)
				return
			}
		}
	}
}
func fn8(m *Module, l0 int32, l1 int32, l2 int32, l3 int32, l4 int32) {
	mBase := m.M
	_ = mBase
	if ui32(l2) < ui32(l1) {
		fn23(m)
		mBase = m.M
		wasm_trap_unreachable()
		for {
		}
	} else {
		if ui32(l4) < ui32(l2) {
			fn23(m)
			mBase = m.M
			wasm_trap_unreachable()
			for {
			}
		} else {
			*(*int32)(unsafe.Add(mBase, uint32(l0)+4)) = l2 - l1
			*(*int32)(unsafe.Add(mBase, uint32(l0))) = l3 + l1
			return
		}
	}
}
func fn9(m *Module, l0 int32, l1 int32, l2 int32, l3 int32) {
	if l1 != l3 {
		fn23(m)
		wasm_trap_unreachable()
		for {
		}
	} else {
		if l1 == int32(0) {
		} else {
			memoryCopy(m, l0, l2, l1)
		}
		return
	}
}
func fn10(m *Module, l0 int32, l1 int32, l2 int32, l3 int32, l4 int32) {
	mBase := m.M
	_ = mBase
	var v18 int32
	_ = v18
	var v20 int32
	_ = v20
	var v22 int64
	_ = v22
	var v30 int32
	_ = v30
	var v32 int32
	_ = v32
	var v33 int32
	_ = v33
	var v34 int32
	_ = v34
	var v35 int32
	_ = v35
	var v39 int32
	_ = v39
	var v41 int32
	_ = v41
	var v43 int32
	_ = v43
	var v49 int32
	_ = v49
	var v50 int32
	_ = v50
	var v56 int32
	_ = v56
	var v57 int32
	_ = v57
	var v63 int32
	_ = v63
	var v66 int32
	_ = v66
	var v68 int32
	_ = v68
	var v70 int32
	_ = v70
	var v76 int32
	_ = v76
	var v77 int32
	_ = v77
	var v83 int32
	_ = v83
	var v84 int32
	_ = v84
	var v86 int32
	_ = v86
	var v94 int32
	_ = v94
	var v95 int32
	_ = v95
	var v96 int32
	_ = v96
	var v110 int32
	_ = v110
	var __phi110 int32
	_ = __phi110
	var v111 int32
	_ = v111
	var __phi111 int32
	_ = __phi111
	var v127 int32
	_ = v127
	var v131 int32
	_ = v131
	var v138 int32
	_ = v138
	var v147 int32
	_ = v147
	var v148 int32
	_ = v148
	var v152 int32
	_ = v152
	var v155 int32
	_ = v155
	var v160 int32
	_ = v160
	var v165 int32
	_ = v165
	var v181 int32
	_ = v181
	var v193 int32
	_ = v193
	var v196 int32
	_ = v196
	v18 = m.g0
	v20 = v18 - int32(48)
	m.g0 = v20
	v22 = int64(0)
	*(*int64)(unsafe.Add(mBase, uint32(v20)+40)) = v22
	*(*int64)(unsafe.Add(mBase, uint32(v20)+33)) = v22
	*(*int64)(unsafe.Add(mBase, uint32(v20)+25)) = v22
	*(*int64)(unsafe.Add(mBase, uint32(v20)+17)) = v22
	v30 = int32(34)
	*(*uint8)(unsafe.Add(mBase, uint32(v20)+16)) = uint8(v30)
	v32 = int32(1)
	v33 = int32(8)
	v34 = v20 + v33
	v35 = int32(16)
	v39 = l4 + v32
	v41 = m.g0
	v43 = v41 - v35
	m.g0 = v43
	fn8(m, v43+v33, v32, v39, v20+v35, int32(32))
	mBase = m.M
	v49 = *(*int32)(unsafe.Add(mBase, uint32(v43)+12))
	v50 = *(*int32)(unsafe.Add(mBase, uint32(v43)+8))
	*(*int32)(unsafe.Add(mBase, uint32(v34))) = v50
	*(*int32)(unsafe.Add(mBase, uint32(v34)+4)) = v49
	m.g0 = v43 + v35
	goto L1
L1:
	;
	v56 = *(*int32)(unsafe.Add(mBase, uint32(v20)+8))
	v57 = *(*int32)(unsafe.Add(mBase, uint32(v20)+12))
	if v57 != l4 {
		goto L3
	} else {
		goto L4
	}
L2:
	;
	v63 = int32(16)
	v66 = l4 + int32(4)
	v68 = m.g0
	v70 = v68 - v63
	m.g0 = v70
	fn8(m, v70+int32(8), v39, v66, v20+v63, int32(32))
	mBase = m.M
	v76 = *(*int32)(unsafe.Add(mBase, uint32(v70)+12))
	v77 = *(*int32)(unsafe.Add(mBase, uint32(v70)+8))
	*(*int32)(unsafe.Add(mBase, uint32(v20))) = v77
	*(*int32)(unsafe.Add(mBase, uint32(v20)+4)) = v76
	m.g0 = v70 + v63
	goto L7
L3:
	;
	fn14(m, v57, l4)
	mBase = m.M
	wasm_trap_unreachable()
	for {
	}
L4:
	;
	if v57 == int32(0) {
		goto L5
	} else {
		goto L6
	}
L5:
	;
	goto L2
L6:
	;
	memoryCopy(m, v56, l3, v57)
	goto L5
L7:
	;
	v83 = *(*int32)(unsafe.Add(mBase, uint32(v20)))
	v84 = *(*int32)(unsafe.Add(mBase, uint32(v20)+4))
	v86 = int32(3)
	if v84 != v86 {
		goto L9
	} else {
		goto L10
	}
L8:
	;
	if ui32(l2) < ui32(v66) {
		v193 = v32
		v196 = int32(0)
		goto L13
	} else {
		goto L14
	}
L9:
	;
	fn14(m, v84, v86)
	mBase = m.M
	wasm_trap_unreachable()
	for {
	}
L10:
	;
	if v84 == int32(0) {
		goto L11
	} else {
		goto L12
	}
L11:
	;
	goto L8
L12:
	;
	memoryCopy(m, v83, int32(1048903), v84)
	goto L11
L13:
	;
	*(*int32)(unsafe.Add(mBase, uint32(l0)+4)) = v196
	*(*int32)(unsafe.Add(mBase, uint32(l0))) = v193
	m.g0 = v20 + int32(48)
	return
L14:
	;
	v94 = l2 - v66
	v95 = int32(0)
	v96 = int32(1)
	__phi110 = v95
	__phi111 = v95
	v110 = __phi110
	v111 = __phi111
	goto L16
L15:
	;
	fn23(m)
	mBase = m.M
	wasm_trap_unreachable()
	for {
	}
L16:
	;
	if v111&int32(1) != 0 {
		v193 = v96
		v196 = v95
		goto L13
	} else {
		goto L18
	}
L17:
	;
	v155 = l1 + (v110 + v66)
	v160 = int32(0)
	v165 = v160
	goto L26
L18:
	;
	if ui32(v94) < ui32(v110) {
		v193 = v96
		v196 = v95
		goto L13
	} else {
		goto L19
	}
L19:
	;
	v127 = v110
	v131 = v66
	v138 = v20 + int32(16)
	goto L21
L20:
	;
	goto L17
L21:
	;
	if v131 == int32(0) {
		goto L20
	} else {
		goto L23
	}
L22:
	;
	__phi110 = v110 + b2i32(ui32(v110) < ui32(v94))
	__phi111 = b2i32(ui32(v94) <= ui32(v110))
	v110 = __phi110
	v111 = __phi111
	goto L16
L23:
	;
	if l2 == v127 {
		goto L15
	} else {
		goto L24
	}
L24:
	;
	v147 = int32(*(*uint8)(unsafe.Add(mBase, uint32(v138))))
	v148 = int32(1)
	v152 = int32(*(*uint8)(unsafe.Add(mBase, uint32(l1+v127))))
	if v147 == v152 {
		v127 = v127 + v148
		v131 = v131 + int32(-1)
		v138 = v138 + v148
		goto L21
	} else {
		goto L25
	}
L25:
	;
	goto L22
L26:
	;
	if l2-l4-v110+int32(-4) == v165 {
		v193 = v155
		v196 = v160
		goto L13
	} else {
		goto L28
	}
L28:
	;
	v181 = int32(*(*uint8)(unsafe.Add(mBase, uint32(v155+v165))))
	if v181 != int32(34) {
		goto L29
	} else {
		goto L30
	}
L29:
	;
	v165 = v165 + int32(1)
	goto L26
L30:
	;
	v193 = v155
	v196 = v165
	goto L13
}
func fn11(m *Module, l0 int32, l1 int32, l2 int32, l3 int32) {
	mBase := m.M
	_ = mBase
	var v6 int32
	_ = v6
	var v8 int32
	_ = v8
	var v11 int32
	_ = v11
	var v12 int32
	_ = v12
	var v20 int32
	_ = v20
	var v21 int32
	_ = v21
	v6 = m.g0
	v8 = v6 - int32(16)
	m.g0 = v8
	v11 = v8 + int32(8)
	v12 = int32(32)
	if ui32(l3) < ui32(l2) {
		fn15(m, l2, l3, v12)
		mBase = m.M
		wasm_trap_unreachable()
		for {
		}
	} else {
		if ui32(v12) < ui32(l3) {
			fn15(m, l2, l3, v12)
			mBase = m.M
			wasm_trap_unreachable()
			for {
			}
		} else {
			*(*int32)(unsafe.Add(mBase, uint32(v11)+4)) = l3 - l2
			*(*int32)(unsafe.Add(mBase, uint32(v11))) = l1 + l2
			v20 = *(*int32)(unsafe.Add(mBase, uint32(v8)+12))
			v21 = *(*int32)(unsafe.Add(mBase, uint32(v8)+8))
			*(*int32)(unsafe.Add(mBase, uint32(l0))) = v21
			*(*int32)(unsafe.Add(mBase, uint32(l0)+4)) = v20
			m.g0 = v8 + int32(16)
			return
		}
	}
}
func fn12(m *Module, l0 int32, l1 int32) {
	wasm_trap_unreachable()
	for {
	}
}
func fn13(m *Module) {
	mBase := m.M
	_ = mBase
	var v4 int32
	_ = v4
	var v12 int32
	_ = v12
	var v15 int32
	_ = v15
	v4 = int32(4644)
	goto L3
L1:
	;
	return
L2:
	;
	v15 = int32(0)
	*(*int32)(unsafe.Add(mBase, _consts[0])) = v15
	goto L1
L3:
	;
	if v4 == int32(153380) {
		goto L2
	} else {
		goto L5
	}
L5:
	;
	v12 = int32(*(*uint8)(unsafe.Add(mBase, uint32(v4)+uint32(_consts[1]))))
	if v12 == int32(0) {
		v4 = v4 + int32(4648)
		goto L3
	} else {
		goto L6
	}
L6:
	;
	goto L1
}
func fn14(m *Module, l0 int32, l1 int32) {
	wasm_trap_unreachable()
	for {
	}
}
func fn15(m *Module, l0 int32, l1 int32, l2 int32) {
	wasm_trap_unreachable()
	for {
	}
}
func fn16(m *Module, l0 int32, l1 int32, l2 int32, l3 int32) {
	mBase := m.M
	_ = mBase
	var v5 int32
	_ = v5
	var v6 int32
	_ = v6
	var v8 int32
	_ = v8
	var v11 int32
	_ = v11
	var v20 int32
	_ = v20
	var v21 int32
	_ = v21
	v5 = int32(0)
	v6 = m.g0
	v8 = v6 - int32(16)
	m.g0 = v8
	v11 = v8 + int32(8)
	if ui32(l1) < ui32(v5) {
		fn15(m, v5, l1, l3)
		mBase = m.M
		wasm_trap_unreachable()
		for {
		}
	} else {
		if ui32(l3) < ui32(l1) {
			fn15(m, v5, l1, l3)
			mBase = m.M
			wasm_trap_unreachable()
			for {
			}
		} else {
			*(*int32)(unsafe.Add(mBase, uint32(v11)+4)) = l1 - v5
			*(*int32)(unsafe.Add(mBase, uint32(v11))) = l2 + v5
			v20 = *(*int32)(unsafe.Add(mBase, uint32(v8)+12))
			v21 = *(*int32)(unsafe.Add(mBase, uint32(v8)+8))
			*(*int32)(unsafe.Add(mBase, uint32(l0))) = v21
			*(*int32)(unsafe.Add(mBase, uint32(l0)+4)) = v20
			m.g0 = v8 + int32(16)
			return
		}
	}
}
func fn17(m *Module, l0 int32) int32 {
	mBase := m.M
	_ = mBase
	var v4 int32
	_ = v4
	var v8 int32
	_ = v8
	var v9 int32
	_ = v9
	v4 = *(*int32)(unsafe.Add(mBase, _consts[0]))
	v8 = (v4 + int32(7)) & int32(-8)
	v9 = v8 + l0
	if ui32(int32(1048576)) < ui32(v9) {
		wasm_trap_unreachable()
		for {
		}
	} else {
		*(*int32)(unsafe.Add(mBase, _consts[0])) = v9
		return v8 + int32(1049172)
	}
}
func fn18(m *Module, l0 int32, l1 int32, l2 int32, l3 int32) int32 {
	mBase := m.M
	_ = mBase
	var v13 int32
	_ = v13
	var v15 int32
	_ = v15
	var v24 int32
	_ = v24
	var v25 int32
	_ = v25
	var v31 int32
	_ = v31
	var v36 int32
	_ = v36
	var v43 int32
	_ = v43
	var v44 int32
	_ = v44
	var v47 int32
	_ = v47
	var v51 int32
	_ = v51
	var v53 int32
	_ = v53
	var v55 int32
	_ = v55
	var v68 int32
	_ = v68
	var v70 int32
	_ = v70
	var v72 int64
	_ = v72
	var v80 int32
	_ = v80
	var v82 int32
	_ = v82
	var v83 int32
	_ = v83
	var v86 int32
	_ = v86
	var v89 int32
	_ = v89
	var v91 int32
	_ = v91
	var v92 int32
	_ = v92
	var v99 int32
	_ = v99
	var v100 int32
	_ = v100
	var v106 int32
	_ = v106
	var v107 int32
	_ = v107
	var v108 int32
	_ = v108
	var v122 int32
	_ = v122
	var __phi122 int32
	_ = __phi122
	var v123 int32
	_ = v123
	var __phi123 int32
	_ = __phi123
	var v139 int32
	_ = v139
	var v143 int32
	_ = v143
	var v150 int32
	_ = v150
	var v159 int32
	_ = v159
	var v160 int32
	_ = v160
	var v164 int32
	_ = v164
	var v167 int32
	_ = v167
	var v172 int32
	_ = v172
	var v177 int32
	_ = v177
	var v193 int32
	_ = v193
	var v205 int32
	_ = v205
	var v208 int32
	_ = v208
	var v221 int32
	_ = v221
	var v222 int32
	_ = v222
	var v225 int32
	_ = v225
	var v227 int32
	_ = v227
	var v229 int32
	_ = v229
	var v232 int32
	_ = v232
	var v233 int32
	_ = v233
	var v234 int32
	_ = v234
	var v240 int32
	_ = v240
	var v241 int32
	_ = v241
	var v247 int32
	_ = v247
	var v248 int32
	_ = v248
	var v256 int32
	_ = v256
	var v257 int32
	_ = v257
	var v265 int32
	_ = v265
	var v266 int32
	_ = v266
	var v273 int32
	_ = v273
	var v274 int32
	_ = v274
	var v276 int32
	_ = v276
	var v289 int32
	_ = v289
	var v291 int32
	_ = v291
	var v293 int64
	_ = v293
	var v301 int32
	_ = v301
	var v303 int32
	_ = v303
	var v307 int32
	_ = v307
	var v310 int32
	_ = v310
	var v312 int32
	_ = v312
	var v313 int32
	_ = v313
	var v318 int32
	_ = v318
	var v320 int32
	_ = v320
	var v321 int32
	_ = v321
	var v327 int32
	_ = v327
	var v328 int32
	_ = v328
	var v329 int32
	_ = v329
	var v343 int32
	_ = v343
	var __phi343 int32
	_ = __phi343
	var v344 int32
	_ = v344
	var __phi344 int32
	_ = __phi344
	var v360 int32
	_ = v360
	var v364 int32
	_ = v364
	var v371 int32
	_ = v371
	var v380 int32
	_ = v380
	var v381 int32
	_ = v381
	var v385 int32
	_ = v385
	var v388 int32
	_ = v388
	var v393 int32
	_ = v393
	var v398 int32
	_ = v398
	var v414 int32
	_ = v414
	var v426 int32
	_ = v426
	var v429 int32
	_ = v429
	var v442 int32
	_ = v442
	var v443 int32
	_ = v443
	var v444 int32
	_ = v444
	var v445 int32
	_ = v445
	var v449 int32
	_ = v449
	var v452 int32
	_ = v452
	var v454 int32
	_ = v454
	var v455 int32
	_ = v455
	var v456 int32
	_ = v456
	var v462 int32
	_ = v462
	var v463 int32
	_ = v463
	var v469 int32
	_ = v469
	var v470 int32
	_ = v470
	var v479 int32
	_ = v479
	var v483 int32
	_ = v483
	var v484 int32
	_ = v484
	var v490 int32
	_ = v490
	var v494 int32
	_ = v494
	var v502 int32
	_ = v502
	v13 = m.g0
	v15 = v13 - int32(48)
	m.g0 = v15
	v24 = int32(0)
	v25 = int32(4644)
	goto L2
L1:
	;
	m.g0 = v15 + int32(48)
	return v502
L2:
	;
	v31 = int32(-4)
	if v25 == int32(153380) {
		v502 = v31
		goto L1
	} else {
		goto L4
	}
L3:
	;
	v43 = int32(4648)
	v44 = v24 * v43
	v47 = int32(0)
	memoryFill(m, v44+int32(2097752), v47, v43)
	v51 = *(*int32)(unsafe.Add(mBase, _consts[2]))
	v53 = v15 + int32(40)
	v55 = int32(4)
	v68 = m.g0
	v70 = v68 - int32(48)
	m.g0 = v70
	v72 = int64(0)
	*(*int64)(unsafe.Add(mBase, uint32(v70)+40)) = v72
	*(*int64)(unsafe.Add(mBase, uint32(v70)+33)) = v72
	*(*int64)(unsafe.Add(mBase, uint32(v70)+25)) = v72
	*(*int64)(unsafe.Add(mBase, uint32(v70)+17)) = v72
	v80 = int32(34)
	*(*uint8)(unsafe.Add(mBase, uint32(v70)+16)) = uint8(v80)
	v82 = int32(1)
	v83 = int32(8)
	v86 = v70 + int32(16)
	v89 = int32(5)
	fn11(m, v70+v83, v86, v82, v89)
	mBase = m.M
	v91 = *(*int32)(unsafe.Add(mBase, uint32(v70)+8))
	v92 = *(*int32)(unsafe.Add(mBase, uint32(v70)+12))
	fn9(m, v91, v92, int32(1048906), v55)
	mBase = m.M
	fn11(m, v70, v86, v89, v83)
	mBase = m.M
	v99 = *(*int32)(unsafe.Add(mBase, uint32(v70)))
	v100 = *(*int32)(unsafe.Add(mBase, uint32(v70)+4))
	fn9(m, v99, v100, int32(1048903), int32(3))
	mBase = m.M
	if ui32(l1) < ui32(v83) {
		v205 = v82
		v208 = v47
		goto L8
	} else {
		goto L9
	}
L4:
	;
	v36 = int32(*(*uint8)(unsafe.Add(mBase, uint32(v25)+uint32(_consts[1]))))
	if v36 == int32(0) {
		goto L5
	} else {
		goto L6
	}
L5:
	;
	goto L3
L6:
	;
	v24 = v24 + int32(1)
	v25 = v25 + int32(4648)
	goto L2
L7:
	;
	v221 = *(*int32)(unsafe.Add(mBase, uint32(v15)+44))
	v222 = v51 + v221
	if ui32(int32(512)) < ui32(v222) {
		v502 = v31
		goto L1
	} else {
		goto L26
	}
L8:
	;
	*(*int32)(unsafe.Add(mBase, uint32(v53)+4)) = v208
	*(*int32)(unsafe.Add(mBase, uint32(v53))) = v205
	m.g0 = v70 + int32(48)
	goto L7
L9:
	;
	v106 = l1 - v83
	v107 = int32(0)
	v108 = int32(1)
	__phi122 = v107
	__phi123 = v107
	v122 = __phi122
	v123 = __phi123
	goto L11
L10:
	;
	fn12(m, l1, l1)
	mBase = m.M
	wasm_trap_unreachable()
	for {
	}
L11:
	;
	if v123&int32(1) != 0 {
		v205 = v108
		v208 = v107
		goto L8
	} else {
		goto L13
	}
L12:
	;
	v167 = l0 + (v122 + v83)
	v172 = int32(0)
	v177 = v172
	goto L21
L13:
	;
	if ui32(v106) < ui32(v122) {
		v205 = v108
		v208 = v107
		goto L8
	} else {
		goto L14
	}
L14:
	;
	v139 = v122
	v143 = v83
	v150 = v70 + int32(16)
	goto L16
L15:
	;
	goto L12
L16:
	;
	if v143 == int32(0) {
		goto L15
	} else {
		goto L18
	}
L17:
	;
	__phi122 = v122 + b2i32(ui32(v122) < ui32(v106))
	__phi123 = b2i32(ui32(v106) <= ui32(v122))
	v122 = __phi122
	v123 = __phi123
	goto L11
L18:
	;
	if l1 == v139 {
		goto L10
	} else {
		goto L19
	}
L19:
	;
	v159 = int32(*(*uint8)(unsafe.Add(mBase, uint32(v150))))
	v160 = int32(1)
	v164 = int32(*(*uint8)(unsafe.Add(mBase, uint32(l0+v139))))
	if v159 == v164 {
		v139 = v139 + v160
		v143 = v143 + int32(-1)
		v150 = v150 + v160
		goto L16
	} else {
		goto L20
	}
L20:
	;
	goto L17
L21:
	;
	if l1-v55-v122+int32(-4) == v177 {
		v205 = v167
		v208 = v172
		goto L8
	} else {
		goto L23
	}
L23:
	;
	v193 = int32(*(*uint8)(unsafe.Add(mBase, uint32(v167+v177))))
	if v193 != int32(34) {
		goto L24
	} else {
		goto L25
	}
L24:
	;
	v177 = v177 + int32(1)
	goto L21
L25:
	;
	v205 = v167
	v208 = v177
	goto L8
L26:
	;
	v225 = *(*int32)(unsafe.Add(mBase, uint32(v15)+40))
	v227 = v15 + int32(32)
	v229 = v44 + int32(2097756)
	v232 = m.g0
	v233 = int32(16)
	v234 = v232 - v233
	m.g0 = v234
	fn8(m, v234+int32(8), int32(0), v51, v229, int32(512))
	mBase = m.M
	v240 = *(*int32)(unsafe.Add(mBase, uint32(v234)+12))
	v241 = *(*int32)(unsafe.Add(mBase, uint32(v234)+8))
	*(*int32)(unsafe.Add(mBase, uint32(v227))) = v241
	*(*int32)(unsafe.Add(mBase, uint32(v227)+4)) = v240
	m.g0 = v234 + v233
	goto L27
L27:
	;
	v247 = *(*int32)(unsafe.Add(mBase, uint32(v15)+32))
	v248 = *(*int32)(unsafe.Add(mBase, uint32(v15)+36))
	if v248 != v51 {
		goto L29
	} else {
		goto L30
	}
L28:
	;
	v256 = v15 + int32(24)
	v257 = int32(512)
	if ui32(v222) < ui32(v51) {
		goto L34
	} else {
		goto L35
	}
L29:
	;
	fn14(m, v248, v51)
	mBase = m.M
	wasm_trap_unreachable()
	for {
	}
L30:
	;
	if v248 == int32(0) {
		goto L31
	} else {
		goto L32
	}
L31:
	;
	goto L28
L32:
	;
	memoryCopy(m, v247, int32(1048916), v248)
	goto L31
L33:
	;
	v265 = *(*int32)(unsafe.Add(mBase, uint32(v15)+24))
	v266 = *(*int32)(unsafe.Add(mBase, uint32(v15)+28))
	if v266 != v221 {
		goto L38
	} else {
		goto L39
	}
L34:
	;
	fn15(m, v51, v222, v257)
	mBase = m.M
	wasm_trap_unreachable()
	for {
	}
L35:
	;
	if ui32(v257) < ui32(v222) {
		goto L34
	} else {
		goto L36
	}
L36:
	;
	*(*int32)(unsafe.Add(mBase, uint32(v256)+4)) = v222 - v51
	*(*int32)(unsafe.Add(mBase, uint32(v256))) = v229 + v51
	goto L33
L37:
	;
	*(*int32)(unsafe.Add(mBase, uint32(v44)+uint32(_consts[3]))) = v222
	v273 = int32(16)
	v274 = v15 + v273
	v276 = int32(6)
	v289 = m.g0
	v291 = v289 - int32(48)
	m.g0 = v291
	v293 = int64(0)
	*(*int64)(unsafe.Add(mBase, uint32(v291)+40)) = v293
	*(*int64)(unsafe.Add(mBase, uint32(v291)+33)) = v293
	*(*int64)(unsafe.Add(mBase, uint32(v291)+25)) = v293
	*(*int64)(unsafe.Add(mBase, uint32(v291)+17)) = v293
	v301 = int32(34)
	*(*uint8)(unsafe.Add(mBase, uint32(v291)+16)) = uint8(v301)
	v303 = int32(1)
	v307 = v291 + v273
	v310 = int32(7)
	fn11(m, v291+int32(8), v307, v303, v310)
	mBase = m.M
	v312 = *(*int32)(unsafe.Add(mBase, uint32(v291)+8))
	v313 = *(*int32)(unsafe.Add(mBase, uint32(v291)+12))
	fn9(m, v312, v313, int32(1048910), v276)
	mBase = m.M
	v318 = int32(10)
	fn11(m, v291, v307, v310, v318)
	mBase = m.M
	v320 = *(*int32)(unsafe.Add(mBase, uint32(v291)))
	v321 = *(*int32)(unsafe.Add(mBase, uint32(v291)+4))
	fn9(m, v320, v321, int32(1048903), int32(3))
	mBase = m.M
	if ui32(l1) < ui32(v318) {
		v426 = v303
		v429 = int32(0)
		goto L43
	} else {
		goto L44
	}
L38:
	;
	fn14(m, v266, v221)
	mBase = m.M
	wasm_trap_unreachable()
	for {
	}
L39:
	;
	if v266 == int32(0) {
		goto L40
	} else {
		goto L41
	}
L40:
	;
	goto L37
L41:
	;
	memoryCopy(m, v265, v225, v266)
	goto L40
L42:
	;
	v442 = *(*int32)(unsafe.Add(mBase, uint32(v15)+16))
	v443 = int32(8)
	v444 = v15 + v443
	v445 = *(*int32)(unsafe.Add(mBase, uint32(v15)+20))
	if ui32(v445) < ui32(v443) {
		goto L61
	} else {
		goto L62
	}
L43:
	;
	*(*int32)(unsafe.Add(mBase, uint32(v274)+4)) = v429
	*(*int32)(unsafe.Add(mBase, uint32(v274))) = v426
	m.g0 = v291 + int32(48)
	goto L42
L44:
	;
	v327 = l1 - v318
	v328 = int32(0)
	v329 = int32(1)
	__phi343 = v328
	__phi344 = v328
	v343 = __phi343
	v344 = __phi344
	goto L46
L45:
	;
	fn12(m, l1, l1)
	mBase = m.M
	wasm_trap_unreachable()
	for {
	}
L46:
	;
	if v344&int32(1) != 0 {
		v426 = v329
		v429 = v328
		goto L43
	} else {
		goto L48
	}
L47:
	;
	v388 = l0 + (v343 + v318)
	v393 = int32(0)
	v398 = v393
	goto L56
L48:
	;
	if ui32(v327) < ui32(v343) {
		v426 = v329
		v429 = v328
		goto L43
	} else {
		goto L49
	}
L49:
	;
	v360 = v343
	v364 = v318
	v371 = v291 + int32(16)
	goto L51
L50:
	;
	goto L47
L51:
	;
	if v364 == int32(0) {
		goto L50
	} else {
		goto L53
	}
L52:
	;
	__phi343 = v343 + b2i32(ui32(v343) < ui32(v327))
	__phi344 = b2i32(ui32(v327) <= ui32(v343))
	v343 = __phi343
	v344 = __phi344
	goto L46
L53:
	;
	if l1 == v360 {
		goto L45
	} else {
		goto L54
	}
L54:
	;
	v380 = int32(*(*uint8)(unsafe.Add(mBase, uint32(v371))))
	v381 = int32(1)
	v385 = int32(*(*uint8)(unsafe.Add(mBase, uint32(l0+v360))))
	if v380 == v385 {
		v360 = v360 + v381
		v364 = v364 + int32(-1)
		v371 = v371 + v381
		goto L51
	} else {
		goto L55
	}
L55:
	;
	goto L52
L56:
	;
	if l1-v276-v343+int32(-4) == v398 {
		v426 = v388
		v429 = v393
		goto L43
	} else {
		goto L58
	}
L58:
	;
	v414 = int32(*(*uint8)(unsafe.Add(mBase, uint32(v388+v398))))
	if v414 != int32(34) {
		goto L59
	} else {
		goto L60
	}
L59:
	;
	v398 = v398 + int32(1)
	goto L56
L60:
	;
	v426 = v388
	v429 = v398
	goto L43
L61:
	;
	v449 = v445
	goto L63
L62:
	;
	v449 = v443
	goto L63
L63:
	;
	v452 = int32(8)
	v454 = m.g0
	v455 = int32(16)
	v456 = v454 - v455
	m.g0 = v456
	fn8(m, v456+v452, int32(0), v449, v44+int32(2098272), v452)
	mBase = m.M
	v462 = *(*int32)(unsafe.Add(mBase, uint32(v456)+12))
	v463 = *(*int32)(unsafe.Add(mBase, uint32(v456)+8))
	*(*int32)(unsafe.Add(mBase, uint32(v444))) = v463
	*(*int32)(unsafe.Add(mBase, uint32(v444)+4)) = v462
	m.g0 = v456 + v455
	goto L64
L64:
	;
	v469 = *(*int32)(unsafe.Add(mBase, uint32(v15)+8))
	v470 = *(*int32)(unsafe.Add(mBase, uint32(v15)+12))
	if v470 != v449 {
		goto L66
	} else {
		goto L67
	}
L65:
	;
	*(*int32)(unsafe.Add(mBase, uint32(v44)+uint32(_consts[4]))) = v449
	v479 = *(*int32)(unsafe.Add(mBase, _consts[0]))
	v483 = (v479 + int32(7)) & int32(-8)
	v484 = v483 + l3
	if ui32(int32(1048576)) < ui32(v484) {
		goto L71
	} else {
		goto L72
	}
L66:
	;
	fn14(m, v470, v449)
	mBase = m.M
	wasm_trap_unreachable()
	for {
	}
L67:
	;
	if v470 == int32(0) {
		goto L68
	} else {
		goto L69
	}
L68:
	;
	goto L65
L69:
	;
	memoryCopy(m, v469, v442, v470)
	goto L68
L70:
	;
	if l3 == int32(0) {
		goto L73
	} else {
		goto L74
	}
L71:
	;
	wasm_trap_unreachable()
	for {
	}
L72:
	;
	*(*int32)(unsafe.Add(mBase, _consts[0])) = v484
	v490 = v483 + int32(1049172)
	goto L70
L73:
	;
	v494 = int32(1)
	*(*uint8)(unsafe.Add(mBase, uint32(v44)+uint32(_consts[5]))) = uint8(v494)
	*(*int32)(unsafe.Add(mBase, uint32(v44)+uint32(_consts[6]))) = l3
	*(*int32)(unsafe.Add(mBase, uint32(v44)+uint32(_consts[7]))) = v490
	v502 = v24 + v494
	goto L1
L74:
	;
	memoryCopy(m, v490, l2, l3)
	goto L73
}
func fn19(m *Module, l0 int32) {
	mBase := m.M
	_ = mBase
	var v8 int32
	_ = v8
	var v13 int32
	_ = v13
	var v19 int32
	_ = v19
	var v24 int32
	_ = v24
	var v32 int32
	_ = v32
	var v35 int32
	_ = v35
	if ui32(l0+int32(-33)) < ui32(int32(-32)) {
		goto L1
	} else {
		goto L2
	}
L1:
	;
	return
L2:
	;
	v8 = l0 * int32(4648)
	v13 = *(*int32)(unsafe.Add(mBase, uint32(v8)+uint32(_consts[8])))
	if v13 < int32(1) {
		goto L3
	} else {
		goto L4
	}
L3:
	;
	v19 = int32(0)
	*(*uint8)(unsafe.Add(mBase, uint32(v8)+uint32(_consts[0]))) = uint8(v19)
	v24 = int32(4644)
	goto L8
L4:
	;
	m.hanzo.Hz_close(m, v13)
	mBase = m.M
	goto L3
L5:
	;
	goto L1
L6:
	;
	goto L5
L7:
	;
	v35 = int32(0)
	*(*int32)(unsafe.Add(mBase, _consts[0])) = v35
	goto L6
L8:
	;
	if v24 == int32(153380) {
		goto L7
	} else {
		goto L10
	}
L10:
	;
	v32 = int32(*(*uint8)(unsafe.Add(mBase, uint32(v24)+uint32(_consts[1]))))
	if v32 == int32(0) {
		v24 = v24 + int32(4648)
		goto L8
	} else {
		goto L11
	}
L11:
	;
	goto L6
}
func fn20(m *Module, l0 int32, l1 int32) {
	return
}
func fn21(m *Module, l0 int32, l1 int32) int32 {
	mBase := m.M
	_ = mBase
	var v11 int32
	_ = v11
	var v14 int32
	_ = v14
	if ui32(int32(256)) < ui32(l1) {
		v14 = int32(-4)
	} else {
		if l1 == int32(0) {
		} else {
			memoryCopy(m, int32(1048916), l0, l1)
		}
		v11 = int32(0)
		*(*int32)(unsafe.Add(mBase, _consts[2])) = l1
		v14 = v11
	}
	return v14
}
func fn22(m *Module, l0 int64) int64 {
	mBase := m.M
	_ = mBase
	var v2 int32
	_ = v2
	var v12 int32
	_ = v12
	var v14 int32
	_ = v14
	var v26 int32
	_ = v26
	var v27 int32
	_ = v27
	var v28 int32
	_ = v28
	var v39 int32
	_ = v39
	var v43 int32
	_ = v43
	var v45 int32
	_ = v45
	var v46 int32
	_ = v46
	var v47 int32
	_ = v47
	var v55 int32
	_ = v55
	var v57 int32
	_ = v57
	var v60 int32
	_ = v60
	var v61 int32
	_ = v61
	var v62 int32
	_ = v62
	var v66 int32
	_ = v66
	var v67 int32
	_ = v67
	var v69 int32
	_ = v69
	var v70 int32
	_ = v70
	var v76 int32
	_ = v76
	var v83 int32
	_ = v83
	var v88 int32
	_ = v88
	var v89 int32
	_ = v89
	var v90 int32
	_ = v90
	var v94 int32
	_ = v94
	var v95 int32
	_ = v95
	var v97 int32
	_ = v97
	var v98 int32
	_ = v98
	var v108 int32
	_ = v108
	var v110 int32
	_ = v110
	var v113 int32
	_ = v113
	var v114 int32
	_ = v114
	var v115 int32
	_ = v115
	var v119 int32
	_ = v119
	var v120 int32
	_ = v120
	var v122 int32
	_ = v122
	var v123 int32
	_ = v123
	var v129 int32
	_ = v129
	var v136 int32
	_ = v136
	var v141 int32
	_ = v141
	var v142 int32
	_ = v142
	var v143 int32
	_ = v143
	var v147 int32
	_ = v147
	var v148 int32
	_ = v148
	var v150 int32
	_ = v150
	var v151 int32
	_ = v151
	var v161 int32
	_ = v161
	var v163 int32
	_ = v163
	var v166 int32
	_ = v166
	var v167 int32
	_ = v167
	var v168 int32
	_ = v168
	var v172 int32
	_ = v172
	var v173 int32
	_ = v173
	var v175 int32
	_ = v175
	var v176 int32
	_ = v176
	var v184 int32
	_ = v184
	var v185 int32
	_ = v185
	var v186 int32
	_ = v186
	var v187 int32
	_ = v187
	var v190 int32
	_ = v190
	var v195 int32
	_ = v195
	var v198 int32
	_ = v198
	var v201 int32
	_ = v201
	var v203 int32
	_ = v203
	var v204 int32
	_ = v204
	var v205 int32
	_ = v205
	var v207 int32
	_ = v207
	var v211 int32
	_ = v211
	var v215 int32
	_ = v215
	var v219 int32
	_ = v219
	var v220 int32
	_ = v220
	var v221 int32
	_ = v221
	var v231 int32
	_ = v231
	var v234 int32
	_ = v234
	var v235 int32
	_ = v235
	var v236 int32
	_ = v236
	var v240 int32
	_ = v240
	var v241 int32
	_ = v241
	var v243 int32
	_ = v243
	var v244 int32
	_ = v244
	var v256 int32
	_ = v256
	var v259 int32
	_ = v259
	var v260 int32
	_ = v260
	var v261 int32
	_ = v261
	var v265 int32
	_ = v265
	var v266 int32
	_ = v266
	var v268 int32
	_ = v268
	var v269 int32
	_ = v269
	var v277 int32
	_ = v277
	var v278 int32
	_ = v278
	var v282 int32
	_ = v282
	var v284 int32
	_ = v284
	var v287 int32
	_ = v287
	var v288 int32
	_ = v288
	var v297 int32
	_ = v297
	var v298 int32
	_ = v298
	var v301 int32
	_ = v301
	var v303 int32
	_ = v303
	var v305 int32
	_ = v305
	var v312 int32
	_ = v312
	var v314 int32
	_ = v314
	var v320 int32
	_ = v320
	var v325 int32
	_ = v325
	var v327 int32
	_ = v327
	var v329 int32
	_ = v329
	var v334 int32
	_ = v334
	var v343 int32
	_ = v343
	var v349 int32
	_ = v349
	var v357 int32
	_ = v357
	var v360 int32
	_ = v360
	var v381 int32
	_ = v381
	var v402 int32
	_ = v402
	v2 = int32(0)
	v12 = m.g0
	v14 = v12 - int32(16608)
	m.g0 = v14
	v26 = v2
	v27 = v2
	v28 = v2
	goto L1
L1:
	;
	if v26 == int32(148736) {
		goto L6
	} else {
		goto L7
	}
L3:
	;
	v26 = v26 + int32(4648)
	v27 = v39
	v28 = v28 | v402
	goto L1
L4:
	;
	v402 = int32(1)
	goto L3
L5:
	;
	v381 = int32(0)
	*(*uint8)(unsafe.Add(mBase, uint32(v26)+uint32(_consts[5]))) = uint8(v381)
	goto L4
L6:
	;
	v343 = int32(0)
	if v28&int32(1) != 0 {
		v26 = v343
		v27 = v343
		v28 = v343
		goto L1
	} else {
		goto L41
	}
L7:
	;
	v39 = v27 + int32(1)
	v43 = int32(*(*uint8)(unsafe.Add(mBase, uint32(v26)+uint32(_consts[5]))))
	switch v43 {
	default:
		v402 = int32(0)
		goto L3
	case 1:
		goto L13
	case 2:
		goto L12
	case 3:
		goto L11
	case 4:
		goto L10
	}
L8:
	;
	fn23(m)
	mBase = m.M
	wasm_trap_unreachable()
	for {
	}
L9:
	;
	fn23(m)
	mBase = m.M
	wasm_trap_unreachable()
	for {
	}
L10:
	;
	v334 = m.hanzo.Hz_end(m, v39, int32(1048832), int32(71))
	mBase = m.M
	goto L5
L11:
	;
	v287 = v26 + int32(2098292)
	v288 = *(*int32)(unsafe.Add(mBase, uint32(v26)+uint32(_consts[9])))
	v297 = int32(0)
	v298 = v288
	goto L28
L12:
	;
	v203 = v14 + int32(12)
	v204 = int32(0)
	v205 = int32(8192)
	memoryFill(m, v203, v204, v205)
	v207 = *(*int32)(unsafe.Add(mBase, uint32(v26)+uint32(_consts[1])))
	v211 = m.hanzo.Hz_head(m, v207, v203, v205)
	mBase = m.M
	v215 = b2i32(ui32(v211+int32(-2)) < ui32(int32(8191)))
	if v215 == v204 {
		v402 = v215
		goto L3
	} else {
		goto L23
	}
L13:
	;
	v45 = v14 + int32(8204)
	v46 = int32(0)
	v47 = int32(768)
	memoryFill(m, v45, v46, v47)
	*(*int32)(unsafe.Add(mBase, uint32(v14)+12)) = v46
	v55 = v14 + int32(12)
	v57 = int32(11)
	v60 = m.g0
	v61 = int32(16)
	v62 = v60 - v61
	m.g0 = v62
	v66 = *(*int32)(unsafe.Add(mBase, uint32(v55)))
	v67 = v66 + v57
	fn8(m, v62+int32(8), v66, v67, v45, v47)
	mBase = m.M
	v69 = *(*int32)(unsafe.Add(mBase, uint32(v62)+8))
	v70 = *(*int32)(unsafe.Add(mBase, uint32(v62)+12))
	fn9(m, v69, v70, int32(1048576), v57)
	mBase = m.M
	*(*int32)(unsafe.Add(mBase, uint32(v55))) = v67
	m.g0 = v62 + v61
	goto L14
L14:
	;
	v76 = *(*int32)(unsafe.Add(mBase, uint32(v26)+uint32(_consts[4])))
	if ui32(int32(9)) <= ui32(v76) {
		goto L8
	} else {
		goto L15
	}
L15:
	;
	v83 = v14 + int32(12)
	v88 = m.g0
	v89 = int32(16)
	v90 = v88 - v89
	m.g0 = v90
	v94 = *(*int32)(unsafe.Add(mBase, uint32(v83)))
	v95 = v94 + v76
	fn8(m, v90+int32(8), v94, v95, v14+int32(8204), int32(768))
	mBase = m.M
	v97 = *(*int32)(unsafe.Add(mBase, uint32(v90)+8))
	v98 = *(*int32)(unsafe.Add(mBase, uint32(v90)+12))
	fn9(m, v97, v98, v26+int32(2098272), v76)
	mBase = m.M
	*(*int32)(unsafe.Add(mBase, uint32(v83))) = v95
	m.g0 = v90 + v89
	goto L16
L16:
	;
	v108 = v14 + int32(12)
	v110 = int32(9)
	v113 = m.g0
	v114 = int32(16)
	v115 = v113 - v114
	m.g0 = v115
	v119 = *(*int32)(unsafe.Add(mBase, uint32(v108)))
	v120 = v119 + v110
	fn8(m, v115+int32(8), v119, v120, v14+int32(8204), int32(768))
	mBase = m.M
	v122 = *(*int32)(unsafe.Add(mBase, uint32(v115)+8))
	v123 = *(*int32)(unsafe.Add(mBase, uint32(v115)+12))
	fn9(m, v122, v123, int32(1048587), v110)
	mBase = m.M
	*(*int32)(unsafe.Add(mBase, uint32(v108))) = v120
	m.g0 = v115 + v114
	goto L17
L17:
	;
	v129 = *(*int32)(unsafe.Add(mBase, uint32(v26)+uint32(_consts[3])))
	if ui32(int32(513)) <= ui32(v129) {
		goto L9
	} else {
		goto L18
	}
L18:
	;
	v136 = v14 + int32(12)
	v141 = m.g0
	v142 = int32(16)
	v143 = v141 - v142
	m.g0 = v143
	v147 = *(*int32)(unsafe.Add(mBase, uint32(v136)))
	v148 = v147 + v129
	fn8(m, v143+int32(8), v147, v148, v14+int32(8204), int32(768))
	mBase = m.M
	v150 = *(*int32)(unsafe.Add(mBase, uint32(v143)+8))
	v151 = *(*int32)(unsafe.Add(mBase, uint32(v143)+12))
	fn9(m, v150, v151, v26+int32(2097756), v129)
	mBase = m.M
	*(*int32)(unsafe.Add(mBase, uint32(v136))) = v148
	m.g0 = v143 + v142
	goto L19
L19:
	;
	v161 = v14 + int32(12)
	v163 = int32(67)
	v166 = m.g0
	v167 = int32(16)
	v168 = v166 - v167
	m.g0 = v168
	v172 = *(*int32)(unsafe.Add(mBase, uint32(v161)))
	v173 = v172 + v163
	fn8(m, v168+int32(8), v172, v173, v14+int32(8204), int32(768))
	mBase = m.M
	v175 = *(*int32)(unsafe.Add(mBase, uint32(v168)+8))
	v176 = *(*int32)(unsafe.Add(mBase, uint32(v168)+12))
	fn9(m, v175, v176, int32(1048596), v163)
	mBase = m.M
	*(*int32)(unsafe.Add(mBase, uint32(v161))) = v173
	m.g0 = v168 + v167
	goto L20
L20:
	;
	v184 = *(*int32)(unsafe.Add(mBase, uint32(v14)+12))
	v185 = *(*int32)(unsafe.Add(mBase, uint32(v26)+uint32(_consts[7])))
	v186 = *(*int32)(unsafe.Add(mBase, uint32(v26)+uint32(_consts[6])))
	v187 = m.hanzo.Hz_open(m, v39, v14+int32(8204), v184, v185, v186)
	mBase = m.M
	if v187 < int32(0) {
		goto L21
	} else {
		goto L22
	}
L21:
	;
	v195 = m.hanzo.Hz_reply(m, v39, int32(1048663), int32(62))
	mBase = m.M
	v198 = m.hanzo.Hz_write(m, v39, int32(1048725), int32(41))
	mBase = m.M
	v201 = m.hanzo.Hz_end(m, v39, int32(1048766), int32(2))
	mBase = m.M
	goto L5
L22:
	;
	v190 = int32(2)
	*(*uint8)(unsafe.Add(mBase, uint32(v26)+uint32(_consts[5]))) = uint8(v190)
	*(*int32)(unsafe.Add(mBase, uint32(v26)+uint32(_consts[1]))) = v187
	goto L4
L23:
	;
	v219 = v14 + int32(8204)
	v220 = int32(0)
	v221 = int32(8400)
	memoryFill(m, v219, v220, v221)
	*(*int32)(unsafe.Add(mBase, uint32(v14)+uint32(_consts[10]))) = v220
	v231 = int32(64)
	v234 = m.g0
	v235 = int32(16)
	v236 = v234 - v235
	m.g0 = v236
	v240 = *(*int32)(unsafe.Add(mBase, uint32(v14)+uint32(_consts[10])))
	v241 = v240 + v231
	fn8(m, v236+int32(8), v240, v241, v219, v221)
	mBase = m.M
	v243 = *(*int32)(unsafe.Add(mBase, uint32(v236)+8))
	v244 = *(*int32)(unsafe.Add(mBase, uint32(v236)+12))
	fn9(m, v243, v244, int32(1048768), v231)
	mBase = m.M
	*(*int32)(unsafe.Add(mBase, uint32(v14)+uint32(_consts[10]))) = v241
	m.g0 = v236 + v235
	goto L24
L24:
	;
	v256 = v211 + int32(-1)
	v259 = m.g0
	v260 = int32(16)
	v261 = v259 - v260
	m.g0 = v261
	v265 = *(*int32)(unsafe.Add(mBase, uint32(v14)+uint32(_consts[10])))
	v266 = v265 + v256
	fn8(m, v261+int32(8), v265, v266, v14+int32(8204), int32(8400))
	mBase = m.M
	v268 = *(*int32)(unsafe.Add(mBase, uint32(v261)+8))
	v269 = *(*int32)(unsafe.Add(mBase, uint32(v261)+12))
	fn9(m, v268, v269, v14+int32(13), v256)
	mBase = m.M
	*(*int32)(unsafe.Add(mBase, uint32(v14)+uint32(_consts[10]))) = v266
	m.g0 = v261 + v260
	goto L25
L25:
	;
	v277 = *(*int32)(unsafe.Add(mBase, uint32(v14)+uint32(_consts[10])))
	v278 = m.hanzo.Hz_reply(m, v39, v14+int32(8204), v277)
	mBase = m.M
	if v278 != int32(-3) {
		goto L26
	} else {
		goto L27
	}
L26:
	;
	v284 = int32(3)
	*(*uint8)(unsafe.Add(mBase, uint32(v26)+uint32(_consts[5]))) = uint8(v284)
	goto L4
L27:
	;
	m.hanzo.Hz_close(m, v207)
	mBase = m.M
	v282 = int32(0)
	*(*uint8)(unsafe.Add(mBase, uint32(v26)+uint32(_consts[5]))) = uint8(v282)
	v402 = v215
	goto L3
L28:
	;
	v301 = *(*int32)(unsafe.Add(mBase, uint32(v26)+uint32(_consts[11])))
	if ui32(v298) < ui32(v301) {
		goto L31
	} else {
		goto L32
	}
L30:
	;
	*(*int32)(unsafe.Add(mBase, uint32(v26)+uint32(_consts[9]))) = v329
	v297 = int32(1)
	v298 = v329
	goto L28
L31:
	;
	v320 = m.hanzo.Hz_write(m, v39, v287+v298, v301-v298)
	mBase = m.M
	if v320 == int32(-1) {
		v402 = v297
		goto L3
	} else {
		goto L36
	}
L32:
	;
	v303 = *(*int32)(unsafe.Add(mBase, uint32(v26)+uint32(_consts[1])))
	v305 = m.hanzo.Hz_read(m, v303, v287, int32(4096))
	mBase = m.M
	if v305 == int32(-1) {
		v402 = v297
		goto L3
	} else {
		goto L33
	}
L33:
	;
	if v305 < int32(1) {
		goto L34
	} else {
		goto L35
	}
L34:
	;
	v312 = *(*int32)(unsafe.Add(mBase, uint32(v26)+uint32(_consts[1])))
	m.hanzo.Hz_close(m, v312)
	mBase = m.M
	v314 = int32(4)
	*(*uint8)(unsafe.Add(mBase, uint32(v26)+uint32(_consts[5]))) = uint8(v314)
	*(*int32)(unsafe.Add(mBase, uint32(v26)+uint32(_consts[1]))) = int32(0)
	goto L4
L35:
	;
	*(*int32)(unsafe.Add(mBase, uint32(v26)+uint32(_consts[11]))) = v305
	v329 = int32(0)
	goto L30
L36:
	;
	if v320 < int32(0) {
		goto L37
	} else {
		goto L38
	}
L37:
	;
	v327 = *(*int32)(unsafe.Add(mBase, uint32(v26)+uint32(_consts[1])))
	m.hanzo.Hz_close(m, v327)
	mBase = m.M
	goto L5
L38:
	;
	v325 = *(*int32)(unsafe.Add(mBase, uint32(v26)+uint32(_consts[9])))
	v329 = v325 + v320
	goto L30
L41:
	;
	v349 = int32(4644)
	goto L45
L42:
	;
	m.g0 = v14 + int32(16608)
	return int64(-1)
L43:
	;
	goto L42
L44:
	;
	v360 = int32(0)
	*(*int32)(unsafe.Add(mBase, _consts[0])) = v360
	goto L43
L45:
	;
	if v349 == int32(153380) {
		goto L44
	} else {
		goto L47
	}
L47:
	;
	v357 = int32(*(*uint8)(unsafe.Add(mBase, uint32(v349)+uint32(_consts[1]))))
	if v357 == int32(0) {
		v349 = v349 + int32(4648)
		goto L45
	} else {
		goto L48
	}
L48:
	;
	goto L43
}
func fn23(m *Module) {
	wasm_trap_unreachable()
	for {
	}
}
