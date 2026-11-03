package checkpoint

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
)

// repr renders an unpickled value like Python's repr (strings quoted the
// Go way), for comparisons in tests. A value already being rendered (a
// cycle) shows as <cycle>.
func repr(v any) string {
	var b strings.Builder
	writeRepr(&b, v, map[any]bool{})
	return b.String()
}

func writeRepr(b *strings.Builder, v any, open map[any]bool) {
	switch v.(type) {
	case *pyTuple, *pyList, *pyDict, *pySet, *pyObject:
		if open[v] {
			b.WriteString("<cycle>")
			return
		}
		open[v] = true
		defer delete(open, v)
	}
	items := func(items []any) {
		for i, item := range items {
			if i > 0 {
				b.WriteString(", ")
			}
			writeRepr(b, item, open)
		}
	}
	pairs := func(d *pyDict) {
		b.WriteString("{")
		for i, k := range d.keys {
			if i > 0 {
				b.WriteString(", ")
			}
			writeRepr(b, k, open)
			b.WriteString(": ")
			writeRepr(b, d.vals[i], open)
		}
		b.WriteString("}")
	}
	switch x := v.(type) {
	case nil:
		b.WriteString("None")
	case bool:
		if x {
			b.WriteString("True")
		} else {
			b.WriteString("False")
		}
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case bigInt:
		b.WriteString("<bigint>")
	case float64:
		b.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
	case string:
		b.WriteString(strconv.Quote(x))
	case pyBytes:
		b.WriteString("b" + strconv.Quote(string(x)))
	case *pyTuple:
		b.WriteString("(")
		items(x.items)
		if len(x.items) == 1 {
			b.WriteString(",")
		}
		b.WriteString(")")
	case *pyList:
		b.WriteString("[")
		items(x.items)
		b.WriteString("]")
	case *pyDict:
		pairs(x)
	case *pySet:
		if x.frozen {
			b.WriteString("frozenset(")
		} else {
			b.WriteString("set(")
		}
		items(x.items)
		b.WriteString(")")
	case *pyGlobal:
		b.WriteString(x.qual)
	case *pyObject:
		if x.class != nil {
			b.WriteString(x.class.qual)
		} else {
			b.WriteString("?")
		}
		b.WriteString("(")
		items(x.args)
		b.WriteString(")")
		if x.attrs != nil {
			pairs(x.attrs)
		}
		if x.state != nil {
			b.WriteString("<")
			writeRepr(b, x.state, open)
			b.WriteString(">")
		}
		if x.items != nil {
			b.WriteString("[")
			items(x.items)
			b.WriteString("]")
		}
		if x.dict != nil {
			b.WriteString("dict")
			pairs(x.dict)
		}
	case *pyTensor:
		if x.param {
			b.WriteString("parameter")
		} else {
			b.WriteString("tensor")
		}
		if !x.known {
			b.WriteString("[?]")
		} else {
			b.WriteString("[")
			for i, d := range x.shape {
				if i > 0 {
					b.WriteString(" ")
				}
				b.WriteString(strconv.FormatInt(d, 10))
			}
			b.WriteString("]")
		}
	case *pyStorage:
		b.WriteString("storage")
	default:
		b.WriteString("<unknown>")
	}
}

func mustUnpickle(t *testing.T, data string) any {
	t.Helper()
	v, err := unpickle([]byte(data), defaultLimits)
	if err != nil {
		t.Fatalf("unpickle: %v", err)
	}
	return v
}

// pythonPickles were written by Python 3.13 (pickle.dumps of the
// structures in testdata/README.md, "Pickles in pickle_test.go").
var pythonPickles = []struct {
	kind     string // scalars or objects
	protocol int
	data     string
}{
	{"scalars", 0, "(dp0\nVa\np1\nI1\nsVbig\np2\nL1180591620717411303424L\nsVneg\np3\nI-5\nsVs\np4\nVx'y\x22z\x5cu005c\x5cu000a\np5\nsVu\np6\nV\x5cu5212\x5cu75d5-\x5cu51f9\x5cu5751\np7\nsVf\np8\nF1.5\nsVt\np9\n(I1\nI2\ntp10\nsVl\np11\n(lp12\nI01\naI00\naNasVb\np13\nc_codecs\nencode\np14\n(V\x5cu0000\xff\np15\nVlatin1\np16\ntp17\nRp18\nsVi2\np19\nI40000\nsVi4\np20\nI-70000\nsVi8\np21\nL1099511627776L\ns."},
	{"scalars", 1, "}q\x00(X\x01\x00\x00\x00aq\x01K\x01X\x03\x00\x00\x00bigq\x02L1180591620717411303424L\nX\x03\x00\x00\x00negq\x03J\xfb\xff\xff\xffX\x01\x00\x00\x00sq\x04X\x07\x00\x00\x00x'y\x22z\x5c\nq\x05X\x01\x00\x00\x00uq\x06X\x0d\x00\x00\x00\xe5\x88\x92\xe7\x97\x95-\xe5\x87\xb9\xe5\x9d\x91q\x07X\x01\x00\x00\x00fq\x08G?\xf8\x00\x00\x00\x00\x00\x00X\x01\x00\x00\x00tq\x09(K\x01K\x02tq\nX\x01\x00\x00\x00lq\x0b]q\x0c(I01\nI00\nNeX\x01\x00\x00\x00bq\x0dc_codecs\nencode\nq\x0e(X\x03\x00\x00\x00\x00\xc3\xbfq\x0fX\x06\x00\x00\x00latin1q\x10tq\x11Rq\x12X\x02\x00\x00\x00i2q\x13M@\x9cX\x02\x00\x00\x00i4q\x14J\x90\xee\xfe\xffX\x02\x00\x00\x00i8q\x15L1099511627776L\nu."},
	{"scalars", 2, "\x80\x02}q\x00(X\x01\x00\x00\x00aq\x01K\x01X\x03\x00\x00\x00bigq\x02\x8a\x09\x00\x00\x00\x00\x00\x00\x00\x00@X\x03\x00\x00\x00negq\x03J\xfb\xff\xff\xffX\x01\x00\x00\x00sq\x04X\x07\x00\x00\x00x'y\x22z\x5c\nq\x05X\x01\x00\x00\x00uq\x06X\x0d\x00\x00\x00\xe5\x88\x92\xe7\x97\x95-\xe5\x87\xb9\xe5\x9d\x91q\x07X\x01\x00\x00\x00fq\x08G?\xf8\x00\x00\x00\x00\x00\x00X\x01\x00\x00\x00tq\x09K\x01K\x02\x86q\nX\x01\x00\x00\x00lq\x0b]q\x0c(\x88\x89NeX\x01\x00\x00\x00bq\x0dc_codecs\nencode\nq\x0eX\x03\x00\x00\x00\x00\xc3\xbfq\x0fX\x06\x00\x00\x00latin1q\x10\x86q\x11Rq\x12X\x02\x00\x00\x00i2q\x13M@\x9cX\x02\x00\x00\x00i4q\x14J\x90\xee\xfe\xffX\x02\x00\x00\x00i8q\x15\x8a\x06\x00\x00\x00\x00\x00\x01u."},
	{"scalars", 3, "\x80\x03}q\x00(X\x01\x00\x00\x00aq\x01K\x01X\x03\x00\x00\x00bigq\x02\x8a\x09\x00\x00\x00\x00\x00\x00\x00\x00@X\x03\x00\x00\x00negq\x03J\xfb\xff\xff\xffX\x01\x00\x00\x00sq\x04X\x07\x00\x00\x00x'y\x22z\x5c\nq\x05X\x01\x00\x00\x00uq\x06X\x0d\x00\x00\x00\xe5\x88\x92\xe7\x97\x95-\xe5\x87\xb9\xe5\x9d\x91q\x07X\x01\x00\x00\x00fq\x08G?\xf8\x00\x00\x00\x00\x00\x00X\x01\x00\x00\x00tq\x09K\x01K\x02\x86q\nX\x01\x00\x00\x00lq\x0b]q\x0c(\x88\x89NeX\x01\x00\x00\x00bq\x0dC\x02\x00\xffq\x0eX\x02\x00\x00\x00i2q\x0fM@\x9cX\x02\x00\x00\x00i4q\x10J\x90\xee\xfe\xffX\x02\x00\x00\x00i8q\x11\x8a\x06\x00\x00\x00\x00\x00\x01u."},
	{"scalars", 4, "\x80\x04\x95\x93\x00\x00\x00\x00\x00\x00\x00}\x94(\x8c\x01a\x94K\x01\x8c\x03big\x94\x8a\x09\x00\x00\x00\x00\x00\x00\x00\x00@\x8c\x03neg\x94J\xfb\xff\xff\xff\x8c\x01s\x94\x8c\x07x'y\x22z\x5c\n\x94\x8c\x01u\x94\x8c\x0d\xe5\x88\x92\xe7\x97\x95-\xe5\x87\xb9\xe5\x9d\x91\x94\x8c\x01f\x94G?\xf8\x00\x00\x00\x00\x00\x00\x8c\x01t\x94K\x01K\x02\x86\x94\x8c\x01l\x94]\x94(\x88\x89Ne\x8c\x01b\x94C\x02\x00\xff\x94\x8c\x02i2\x94M@\x9c\x8c\x02i4\x94J\x90\xee\xfe\xff\x8c\x02i8\x94\x8a\x06\x00\x00\x00\x00\x00\x01u."},
	{"scalars", 5, "\x80\x05\x95\x93\x00\x00\x00\x00\x00\x00\x00}\x94(\x8c\x01a\x94K\x01\x8c\x03big\x94\x8a\x09\x00\x00\x00\x00\x00\x00\x00\x00@\x8c\x03neg\x94J\xfb\xff\xff\xff\x8c\x01s\x94\x8c\x07x'y\x22z\x5c\n\x94\x8c\x01u\x94\x8c\x0d\xe5\x88\x92\xe7\x97\x95-\xe5\x87\xb9\xe5\x9d\x91\x94\x8c\x01f\x94G?\xf8\x00\x00\x00\x00\x00\x00\x8c\x01t\x94K\x01K\x02\x86\x94\x8c\x01l\x94]\x94(\x88\x89Ne\x8c\x01b\x94C\x02\x00\xff\x94\x8c\x02i2\x94M@\x9c\x8c\x02i4\x94J\x90\xee\xfe\xff\x8c\x02i8\x94\x8a\x06\x00\x00\x00\x00\x00\x01u."},
	{"objects", 0, "(dp0\nVthing\np1\nccopy_reg\n_reconstructor\np2\n(cfakemod\nThing\np3\nc__builtin__\nobject\np4\nNtp5\nRp6\n(dp7\nVx\np8\nI1\nsVme\np9\ng6\nsbsVpair\np10\ng2\n(cfakemod\nPair\np11\ng4\nNtp12\nRp13\n(N(dp14\nVa\np15\nI1\nsVb\np16\nVtwo\np17\nstp18\nbsVkw\np19\ng2\n(cfakemod\nKeyword\np20\ng4\nNtp21\nRp22\n(dp23\nVargs\np24\n(I1\ntp25\nsVkwargs\np26\n(dp27\nVk\np28\nI2\nssbsVod\np29\nccollections\nOrderedDict\np30\n(tRp31\ng28\nI1\nssVdd\np32\nccollections\ndefaultdict\np33\n(c__builtin__\nlist\np34\ntp35\nRp36\nVz\np37\n(lp38\nI1\nassVset\np39\nc__builtin__\nset\np40\n((lp41\nI3\natp42\nRp43\nsVfs\np44\nc__builtin__\nfrozenset\np45\n((lp46\nI4\natp47\nRp48\nsVba\np49\nc__builtin__\nbytearray\np50\n(c_codecs\nencode\np51\n(Vab\np52\nVlatin1\np53\ntp54\nRp55\ntp56\nRp57\nsVnp\np58\n(lp59\ncnumpy._core.multiarray\nscalar\np60\n(cnumpy\ndtype\np61\n(Vf8\np62\nI00\nI01\ntp63\nRp64\n(I3\nV<\np65\nNNNI-1\nI-1\nI0\ntp66\nbg51\n(V\x5cu0000\x5cu0000\x5cu0000\x5cu0000\x5cu0000\x5cu0000\xd0?\np67\ng53\ntp68\nRp69\ntp70\nRp71\nag60\n(g61\n(Vf4\np72\nI00\nI01\ntp73\nRp74\n(I3\ng65\nNNNI-1\nI-1\nI0\ntp75\nbg51\n(V\x5cu0000\x5cu0000\xc0?\np76\ng53\ntp77\nRp78\ntp79\nRp80\nag60\n(g61\n(Vi8\np81\nI00\nI01\ntp82\nRp83\n(I3\ng65\nNNNI-1\nI-1\nI0\ntp84\nbg51\n(V\xfd\xff\xff\xff\xff\xff\xff\xff\np85\ng53\ntp86\nRp87\ntp88\nRp89\nag60\n(g61\n(Vu1\np90\nI00\nI01\ntp91\nRp92\n(I3\nV|\np93\nNNNI-1\nI-1\nI0\ntp94\nbg51\n(V\xc8\np95\ng53\ntp96\nRp97\ntp98\nRp99\nag60\n(g61\n(Vb1\np100\nI00\nI01\ntp101\nRp102\n(I3\ng93\nNNNI-1\nI-1\nI0\ntp103\nbg51\n(V\x01\np104\ng53\ntp105\nRp106\ntp107\nRp108\nag60\n(g61\n(Vf2\np109\nI00\nI01\ntp110\nRp111\n(I3\ng65\nNNNI-1\nI-1\nI0\ntp112\nbg51\n(V\x5cu00008\np113\ng53\ntp114\nRp115\ntp116\nRp117\nasVpath\np118\ncpathlib._local\nPurePosixPath\np119\n(V/data/a.yaml\np120\ntp121\nRp122\nsVwpath\np123\ncpathlib._local\nPureWindowsPath\np124\n(VC:\x5cu005cdata\x5cu005ca.yaml\np125\ntp126\nRp127\ns."},
	{"objects", 1, "}q\x00(X\x05\x00\x00\x00thingq\x01ccopy_reg\n_reconstructor\nq\x02(cfakemod\nThing\nq\x03c__builtin__\nobject\nq\x04Ntq\x05Rq\x06}q\x07(X\x01\x00\x00\x00xq\x08K\x01X\x02\x00\x00\x00meq\x09h\x06ubX\x04\x00\x00\x00pairq\nh\x02(cfakemod\nPair\nq\x0bh\x04Ntq\x0cRq\x0d(N}q\x0e(X\x01\x00\x00\x00aq\x0fK\x01X\x01\x00\x00\x00bq\x10X\x03\x00\x00\x00twoq\x11utq\x12bX\x02\x00\x00\x00kwq\x13h\x02(cfakemod\nKeyword\nq\x14h\x04Ntq\x15Rq\x16}q\x17(X\x04\x00\x00\x00argsq\x18(K\x01tq\x19X\x06\x00\x00\x00kwargsq\x1a}q\x1bX\x01\x00\x00\x00kq\x1cK\x02subX\x02\x00\x00\x00odq\x1dccollections\nOrderedDict\nq\x1e)Rq\x1fh\x1cK\x01sX\x02\x00\x00\x00ddq ccollections\ndefaultdict\nq!(c__builtin__\nlist\nq\x22tq#Rq$X\x01\x00\x00\x00zq%]q&K\x01asX\x03\x00\x00\x00setq'c__builtin__\nset\nq((]q)K\x03atq*Rq+X\x02\x00\x00\x00fsq,c__builtin__\nfrozenset\nq-(]q.K\x04atq/Rq0X\x02\x00\x00\x00baq1c__builtin__\nbytearray\nq2(c_codecs\nencode\nq3(X\x02\x00\x00\x00abq4X\x06\x00\x00\x00latin1q5tq6Rq7tq8Rq9X\x02\x00\x00\x00npq:]q;(cnumpy._core.multiarray\nscalar\nq<(cnumpy\ndtype\nq=(X\x02\x00\x00\x00f8q>I00\nI01\ntq?Rq@(K\x03X\x01\x00\x00\x00<qANNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00tqBbh3(X\x09\x00\x00\x00\x00\x00\x00\x00\x00\x00\xc3\x90?qCh5tqDRqEtqFRqGh<(h=(X\x02\x00\x00\x00f4qHI00\nI01\ntqIRqJ(K\x03hANNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00tqKbh3(X\x05\x00\x00\x00\x00\x00\xc3\x80?qLh5tqMRqNtqORqPh<(h=(X\x02\x00\x00\x00i8qQI00\nI01\ntqRRqS(K\x03hANNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00tqTbh3(X\x10\x00\x00\x00\xc3\xbd\xc3\xbf\xc3\xbf\xc3\xbf\xc3\xbf\xc3\xbf\xc3\xbf\xc3\xbfqUh5tqVRqWtqXRqYh<(h=(X\x02\x00\x00\x00u1qZI00\nI01\ntq[Rq\x5c(K\x03X\x01\x00\x00\x00|q]NNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00tq^bh3(X\x02\x00\x00\x00\xc3\x88q_h5tq`RqatqbRqch<(h=(X\x02\x00\x00\x00b1qdI00\nI01\ntqeRqf(K\x03h]NNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00tqgbh3(X\x01\x00\x00\x00\x01qhh5tqiRqjtqkRqlh<(h=(X\x02\x00\x00\x00f2qmI00\nI01\ntqnRqo(K\x03hANNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00tqpbh3(X\x02\x00\x00\x00\x008qqh5tqrRqstqtRqueX\x04\x00\x00\x00pathqvcpathlib._local\nPurePosixPath\nqw(X\x0c\x00\x00\x00/data/a.yamlqxtqyRqzX\x05\x00\x00\x00wpathq{cpathlib._local\nPureWindowsPath\nq|(X\x0e\x00\x00\x00C:\x5cdata\x5ca.yamlq}tq~Rq\x7fu."},
	{"objects", 2, "\x80\x02}q\x00(X\x05\x00\x00\x00thingq\x01cfakemod\nThing\nq\x02)\x81q\x03}q\x04(X\x01\x00\x00\x00xq\x05K\x01X\x02\x00\x00\x00meq\x06h\x03ubX\x04\x00\x00\x00pairq\x07cfakemod\nPair\nq\x08)\x81q\x09N}q\n(X\x01\x00\x00\x00aq\x0bK\x01X\x01\x00\x00\x00bq\x0cX\x03\x00\x00\x00twoq\x0du\x86q\x0ebX\x02\x00\x00\x00kwq\x0fcfunctools\npartial\nq\x10c__builtin__\ngetattr\nq\x11c__main__\nKeyword\nX\x07\x00\x00\x00__new__q\x12\x86Rq\x13\x85q\x14Rq\x15(h\x13cfakemod\nKeyword\nq\x16K\x01\x86q\x17}q\x18X\x01\x00\x00\x00kq\x19K\x02sNtq\x1ab)Rq\x1b}q\x1c(X\x04\x00\x00\x00argsq\x1dK\x01\x85q\x1eX\x06\x00\x00\x00kwargsq\x1f}q h\x19K\x02subX\x02\x00\x00\x00odq!ccollections\nOrderedDict\nq\x22)Rq#h\x19K\x01sX\x02\x00\x00\x00ddq$ccollections\ndefaultdict\nq%c__builtin__\nlist\nq&\x85q'Rq(X\x01\x00\x00\x00zq)]q*K\x01asX\x03\x00\x00\x00setq+c__builtin__\nset\nq,]q-K\x03a\x85q.Rq/X\x02\x00\x00\x00fsq0c__builtin__\nfrozenset\nq1]q2K\x04a\x85q3Rq4X\x02\x00\x00\x00baq5c__builtin__\nbytearray\nq6c_codecs\nencode\nq7X\x02\x00\x00\x00abq8X\x06\x00\x00\x00latin1q9\x86q:Rq;\x85q<Rq=X\x02\x00\x00\x00npq>]q?(cnumpy._core.multiarray\nscalar\nq@cnumpy\ndtype\nqAX\x02\x00\x00\x00f8qB\x89\x88\x87qCRqD(K\x03X\x01\x00\x00\x00<qENNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00tqFbh7X\x09\x00\x00\x00\x00\x00\x00\x00\x00\x00\xc3\x90?qGh9\x86qHRqI\x86qJRqKh@hAX\x02\x00\x00\x00f4qL\x89\x88\x87qMRqN(K\x03hENNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00tqObh7X\x05\x00\x00\x00\x00\x00\xc3\x80?qPh9\x86qQRqR\x86qSRqTh@hAX\x02\x00\x00\x00i8qU\x89\x88\x87qVRqW(K\x03hENNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00tqXbh7X\x10\x00\x00\x00\xc3\xbd\xc3\xbf\xc3\xbf\xc3\xbf\xc3\xbf\xc3\xbf\xc3\xbf\xc3\xbfqYh9\x86qZRq[\x86q\x5cRq]h@hAX\x02\x00\x00\x00u1q^\x89\x88\x87q_Rq`(K\x03X\x01\x00\x00\x00|qaNNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00tqbbh7X\x02\x00\x00\x00\xc3\x88qch9\x86qdRqe\x86qfRqgh@hAX\x02\x00\x00\x00b1qh\x89\x88\x87qiRqj(K\x03haNNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00tqkbh7X\x01\x00\x00\x00\x01qlh9\x86qmRqn\x86qoRqph@hAX\x02\x00\x00\x00f2qq\x89\x88\x87qrRqs(K\x03hENNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00tqtbh7X\x02\x00\x00\x00\x008quh9\x86qvRqw\x86qxRqyeX\x04\x00\x00\x00pathqzcpathlib._local\nPurePosixPath\nq{X\x0c\x00\x00\x00/data/a.yamlq|\x85q}Rq~X\x05\x00\x00\x00wpathq\x7fcpathlib._local\nPureWindowsPath\nq\x80X\x0e\x00\x00\x00C:\x5cdata\x5ca.yamlq\x81\x85q\x82Rq\x83u."},
	{"objects", 3, "\x80\x03}q\x00(X\x05\x00\x00\x00thingq\x01cfakemod\nThing\nq\x02)\x81q\x03}q\x04(X\x01\x00\x00\x00xq\x05K\x01X\x02\x00\x00\x00meq\x06h\x03ubX\x04\x00\x00\x00pairq\x07cfakemod\nPair\nq\x08)\x81q\x09N}q\n(X\x01\x00\x00\x00aq\x0bK\x01X\x01\x00\x00\x00bq\x0cX\x03\x00\x00\x00twoq\x0du\x86q\x0ebX\x02\x00\x00\x00kwq\x0fcfunctools\npartial\nq\x10cbuiltins\ngetattr\nq\x11c__main__\nKeyword\nX\x07\x00\x00\x00__new__q\x12\x86Rq\x13\x85q\x14Rq\x15(h\x13cfakemod\nKeyword\nq\x16K\x01\x86q\x17}q\x18X\x01\x00\x00\x00kq\x19K\x02sNtq\x1ab)Rq\x1b}q\x1c(X\x04\x00\x00\x00argsq\x1dK\x01\x85q\x1eX\x06\x00\x00\x00kwargsq\x1f}q h\x19K\x02subX\x02\x00\x00\x00odq!ccollections\nOrderedDict\nq\x22)Rq#h\x19K\x01sX\x02\x00\x00\x00ddq$ccollections\ndefaultdict\nq%cbuiltins\nlist\nq&\x85q'Rq(X\x01\x00\x00\x00zq)]q*K\x01asX\x03\x00\x00\x00setq+cbuiltins\nset\nq,]q-K\x03a\x85q.Rq/X\x02\x00\x00\x00fsq0cbuiltins\nfrozenset\nq1]q2K\x04a\x85q3Rq4X\x02\x00\x00\x00baq5cbuiltins\nbytearray\nq6C\x02abq7\x85q8Rq9X\x02\x00\x00\x00npq:]q;(cnumpy._core.multiarray\nscalar\nq<cnumpy\ndtype\nq=X\x02\x00\x00\x00f8q>\x89\x88\x87q?Rq@(K\x03X\x01\x00\x00\x00<qANNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00tqBbC\x08\x00\x00\x00\x00\x00\x00\xd0?qC\x86qDRqEh<h=X\x02\x00\x00\x00f4qF\x89\x88\x87qGRqH(K\x03hANNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00tqIbC\x04\x00\x00\xc0?qJ\x86qKRqLh<h=X\x02\x00\x00\x00i8qM\x89\x88\x87qNRqO(K\x03hANNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00tqPbC\x08\xfd\xff\xff\xff\xff\xff\xff\xffqQ\x86qRRqSh<h=X\x02\x00\x00\x00u1qT\x89\x88\x87qURqV(K\x03X\x01\x00\x00\x00|qWNNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00tqXbC\x01\xc8qY\x86qZRq[h<h=X\x02\x00\x00\x00b1q\x5c\x89\x88\x87q]Rq^(K\x03hWNNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00tq_bC\x01\x01q`\x86qaRqbh<h=X\x02\x00\x00\x00f2qc\x89\x88\x87qdRqe(K\x03hANNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00tqfbC\x02\x008qg\x86qhRqieX\x04\x00\x00\x00pathqjcpathlib._local\nPurePosixPath\nqkX\x0c\x00\x00\x00/data/a.yamlql\x85qmRqnX\x05\x00\x00\x00wpathqocpathlib._local\nPureWindowsPath\nqpX\x0e\x00\x00\x00C:\x5cdata\x5ca.yamlqq\x85qrRqsu."},
	{"objects", 4, "\x80\x04\x95\x17\x03\x00\x00\x00\x00\x00\x00}\x94(\x8c\x05thing\x94\x8c\x07fakemod\x94\x8c\x05Thing\x94\x93\x94)\x81\x94}\x94(\x8c\x01x\x94K\x01\x8c\x02me\x94h\x05ub\x8c\x04pair\x94h\x02\x8c\x04Pair\x94\x93\x94)\x81\x94N}\x94(\x8c\x01a\x94K\x01\x8c\x01b\x94\x8c\x03two\x94u\x86\x94b\x8c\x02kw\x94h\x02\x8c\x07Keyword\x94\x93\x94K\x01\x85\x94}\x94\x8c\x01k\x94K\x02s\x92\x94}\x94(\x8c\x04args\x94h\x15\x8c\x06kwargs\x94h\x16ub\x8c\x02od\x94\x8c\x0bcollections\x94\x8c\x0bOrderedDict\x94\x93\x94)R\x94h\x17K\x01s\x8c\x02dd\x94\x8c\x0bcollections\x94\x8c\x0bdefaultdict\x94\x93\x94\x8c\x08builtins\x94\x8c\x04list\x94\x93\x94\x85\x94R\x94\x8c\x01z\x94]\x94K\x01as\x8c\x03set\x94\x8f\x94(K\x03\x90\x8c\x02fs\x94(K\x04\x91\x94\x8c\x02ba\x94h%\x8c\x09bytearray\x94\x93\x94C\x02ab\x94\x85\x94R\x94\x8c\x02np\x94]\x94(\x8c\x16numpy._core.multiarray\x94\x8c\x06scalar\x94\x93\x94\x8c\x05numpy\x94\x8c\x05dtype\x94\x93\x94\x8c\x02f8\x94\x89\x88\x87\x94R\x94(K\x03\x8c\x01<\x94NNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00t\x94bC\x08\x00\x00\x00\x00\x00\x00\xd0?\x94\x86\x94R\x94h:h=\x8c\x02f4\x94\x89\x88\x87\x94R\x94(K\x03hANNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00t\x94bC\x04\x00\x00\xc0?\x94\x86\x94R\x94h:h=\x8c\x02i8\x94\x89\x88\x87\x94R\x94(K\x03hANNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00t\x94bC\x08\xfd\xff\xff\xff\xff\xff\xff\xff\x94\x86\x94R\x94h:h=\x8c\x02u1\x94\x89\x88\x87\x94R\x94(K\x03\x8c\x01|\x94NNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00t\x94bC\x01\xc8\x94\x86\x94R\x94h:h=\x8c\x02b1\x94\x89\x88\x87\x94R\x94(K\x03hWNNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00t\x94bC\x01\x01\x94\x86\x94R\x94h:h=\x8c\x02f2\x94\x89\x88\x87\x94R\x94(K\x03hANNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00t\x94bC\x02\x008\x94\x86\x94R\x94e\x8c\x04path\x94\x8c\x0epathlib._local\x94\x8c\x0dPurePosixPath\x94\x93\x94\x8c\x0c/data/a.yaml\x94\x85\x94R\x94\x8c\x05wpath\x94hk\x8c\x0fPureWindowsPath\x94\x93\x94\x8c\x0eC:\x5cdata\x5ca.yaml\x94\x85\x94R\x94u."},
	{"objects", 5, "\x80\x05\x95\n\x03\x00\x00\x00\x00\x00\x00}\x94(\x8c\x05thing\x94\x8c\x07fakemod\x94\x8c\x05Thing\x94\x93\x94)\x81\x94}\x94(\x8c\x01x\x94K\x01\x8c\x02me\x94h\x05ub\x8c\x04pair\x94h\x02\x8c\x04Pair\x94\x93\x94)\x81\x94N}\x94(\x8c\x01a\x94K\x01\x8c\x01b\x94\x8c\x03two\x94u\x86\x94b\x8c\x02kw\x94h\x02\x8c\x07Keyword\x94\x93\x94K\x01\x85\x94}\x94\x8c\x01k\x94K\x02s\x92\x94}\x94(\x8c\x04args\x94h\x15\x8c\x06kwargs\x94h\x16ub\x8c\x02od\x94\x8c\x0bcollections\x94\x8c\x0bOrderedDict\x94\x93\x94)R\x94h\x17K\x01s\x8c\x02dd\x94\x8c\x0bcollections\x94\x8c\x0bdefaultdict\x94\x93\x94\x8c\x08builtins\x94\x8c\x04list\x94\x93\x94\x85\x94R\x94\x8c\x01z\x94]\x94K\x01as\x8c\x03set\x94\x8f\x94(K\x03\x90\x8c\x02fs\x94(K\x04\x91\x94\x8c\x02ba\x94\x96\x02\x00\x00\x00\x00\x00\x00\x00ab\x94\x8c\x02np\x94]\x94(\x8c\x16numpy._core.multiarray\x94\x8c\x06scalar\x94\x93\x94\x8c\x05numpy\x94\x8c\x05dtype\x94\x93\x94\x8c\x02f8\x94\x89\x88\x87\x94R\x94(K\x03\x8c\x01<\x94NNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00t\x94bC\x08\x00\x00\x00\x00\x00\x00\xd0?\x94\x86\x94R\x94h6h9\x8c\x02f4\x94\x89\x88\x87\x94R\x94(K\x03h=NNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00t\x94bC\x04\x00\x00\xc0?\x94\x86\x94R\x94h6h9\x8c\x02i8\x94\x89\x88\x87\x94R\x94(K\x03h=NNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00t\x94bC\x08\xfd\xff\xff\xff\xff\xff\xff\xff\x94\x86\x94R\x94h6h9\x8c\x02u1\x94\x89\x88\x87\x94R\x94(K\x03\x8c\x01|\x94NNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00t\x94bC\x01\xc8\x94\x86\x94R\x94h6h9\x8c\x02b1\x94\x89\x88\x87\x94R\x94(K\x03hSNNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00t\x94bC\x01\x01\x94\x86\x94R\x94h6h9\x8c\x02f2\x94\x89\x88\x87\x94R\x94(K\x03h=NNNJ\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00t\x94bC\x02\x008\x94\x86\x94R\x94e\x8c\x04path\x94\x8c\x0epathlib._local\x94\x8c\x0dPurePosixPath\x94\x93\x94\x8c\x0c/data/a.yaml\x94\x85\x94R\x94\x8c\x05wpath\x94hg\x8c\x0fPureWindowsPath\x94\x93\x94\x8c\x0eC:\x5cdata\x5ca.yaml\x94\x85\x94R\x94u."},
}

const wantScalars = `{"a": 1, "big": <bigint>, "neg": -5, "s": "x'y\"z\\\n", "u": "划痕-凹坑", "f": 1.5, "t": (1, 2), ` +
	`"l": [True, False, None], "b": b"\x00\xff", "i2": 40000, "i4": -70000, "i8": 1099511627776}`

func TestUnpicklePythonScalars(t *testing.T) {
	for _, p := range pythonPickles {
		if p.kind != "scalars" {
			continue
		}
		t.Run("protocol"+strconv.Itoa(p.protocol), func(t *testing.T) {
			if got := repr(mustUnpickle(t, p.data)); got != wantScalars {
				t.Errorf("got  %s\nwant %s", got, wantScalars)
			}
		})
	}
}

func TestUnpicklePythonObjects(t *testing.T) {
	for _, p := range pythonPickles {
		if p.kind != "objects" {
			continue
		}
		t.Run("protocol"+strconv.Itoa(p.protocol), func(t *testing.T) {
			d, ok := mustUnpickle(t, p.data).(*pyDict)
			if !ok {
				t.Fatal("not a dict")
			}
			get := func(k string) any {
				v, ok := d.get(k)
				if !ok {
					t.Fatalf("missing %q", k)
				}
				return v
			}
			// An instance whose __dict__ refers back to it.
			thing, ok := get("thing").(*pyObject)
			if !ok || thing.class == nil || thing.class.qual != "fakemod.Thing" {
				t.Fatalf("thing = %s", repr(get("thing")))
			}
			if me, _ := thing.attrs.get("me"); me != thing {
				t.Errorf("thing.me is not thing: %s", repr(thing))
			}
			if got := repr(thing); got != `fakemod.Thing(){"x": 1, "me": <cycle>}` && got != `fakemod.Thing(fakemod.Thing, builtins.object, None){"x": 1, "me": <cycle>}` {
				t.Errorf("thing = %s", got)
			}
			// Slots: BUILD with (None, {slot: value}).
			pair, _ := get("pair").(*pyObject)
			if a, _ := lookup(pair, "a"); a != int64(1) {
				t.Errorf("pair = %s", repr(get("pair")))
			}
			if b, _ := lookup(pair, "b"); b != "two" {
				t.Errorf("pair = %s", repr(get("pair")))
			}
			// __getnewargs_ex__: NEWOBJ_EX from protocol 4 on; below, a
			// functools.partial of cls.__new__ that only becomes a placeholder.
			if kw, ok := get("kw").(*pyObject); !ok {
				t.Errorf("kw = %s", repr(get("kw")))
			} else if p.protocol >= 4 {
				if got := repr(kw); got != `fakemod.Keyword(1){"args": (1,), "kwargs": {"k": 2}}` {
					t.Errorf("kw = %s", got)
				}
			}
			for key, want := range map[string]string{
				"od":    `{"k": 1}`,
				"dd":    `{"z": [1]}`,
				"set":   `set(3)`,
				"fs":    `frozenset(4)`,
				"ba":    `b"ab"`,
				"np":    `[0.25, 1.5, -3, 200, True, 0.5]`,
				"path":  `"/data/a.yaml"`,
				"wpath": `"C:\\data\\a.yaml"`,
			} {
				if got := repr(get(key)); got != want {
					t.Errorf("%s = %s, want %s", key, got, want)
				}
			}
		})
	}
}

func TestUnpickleOpcodes(t *testing.T) {
	tests := []struct {
		name, data, want string
	}{
		// Protocols 0 and 1.
		{"INST and BUILD", "(i__main__\nPoint\n(dp0\nVx\np1\nI3\nsb.", `__main__.Point(){"x": 3}`},
		{"OBJ", "(c__main__\nPoint\nI1\nI2\no.", `__main__.Point(1, 2)`},
		{"STRING escapes", "S'a\\'b\\x41\\101\\n\\q'\n.", `"a'bAA\n\\q"`},
		{"STRING double quotes", "S\"it's\"\n.", `"it's"`},
		{"INT, LONG and FLOAT", "(I01\nI00\nI-7\nI0x10\nI99999999999999999999\nL5L\nF-2.5\nFnan\nF1e999\nt.",
			`(True, False, -7, 16, <bigint>, 5, -2.5, NaN, +Inf)`},
		{"UNICODE escapes", "V\\u5212\\U0001f600\\U00110000x\\q\n.", `"划😀�x\\q"`},
		{"binary ints and float", "(K\xffM\xff\xffJ\xff\xff\xff\xffG\xbf\xf0\x00\x00\x00\x00\x00\x00t.", `(255, 65535, -1, -1)`},
		{"memo", "(K\x07p3\n0g3\nh\x03\x94r\xff\xff\xff\xffj\xff\xff\xff\xfft.", `(7, 7, 7)`},
		{"POP_MARK and DUP", "(K\x01(K\x021K\x032t.", `(1, 3, 3)`},
		{"POP on an empty frame discards the MARK", "K\x01(0.", `1`},
		{"PERSID and BINPERSID", "(P0\nK\x00Qt.", `(storage, storage)`},
		{"GLOBAL maps Python 2 module names", "c__builtin__\nset\n]K\x01a\x85R.", `set(1)`},
		{"copy_reg._reconstructor", "ccopy_reg\n_reconstructor\n(c__main__\nP\nc__builtin__\nobject\nNtR}K\x01K\x02sb.",
			`__main__.P(){1: 2}`},

		// Protocols 2–5.
		{"LONG1 and LONG4", "\x80\x02(\x8a\x00\x8a\x01\xff\x8a\x02\x00\x80\x8a\x08\xff\xff\xff\xff\xff\xff\xff\x7f" +
			"\x8b\x01\x00\x00\x00\x05\x8a\x09\x00\x00\x00\x00\x00\x00\x00\x00\x01t.",
			`(0, -1, -32768, 9223372036854775807, 5, <bigint>)`},
		{"strings and bytes", "(T\x02\x00\x00\x00abU\x01cB\x01\x00\x00\x00dC\x01e\x8e\x01\x00\x00\x00\x00\x00\x00\x00f" +
			"\x96\x01\x00\x00\x00\x00\x00\x00\x00g\x98\x8c\x01h\x8d\x01\x00\x00\x00\x00\x00\x00\x00it.",
			`("ab", "c", b"d", b"e", b"f", b"g", "h", "i")`},
		{"containers", "\x80\x04((K\x01K\x02d(K\x03l\x8f(K\x04\x90(K\x05\x91K\x06\x85K\x07K\x08\x86K\x09K\x0aK\x0b\x87t.",
			`({1: 2}, [3], set(4), frozenset(5), (6,), (7, 8), (9, 10, 11))`},
		{"FRAME, STACK_GLOBAL and NEWOBJ_EX", "\x80\x04\x95\x00\x00\x00\x00\x00\x00\x00\x00\x8c\x07fakemod\x8c\x05Thing\x93" +
			"K\x01\x85}\x92.", `fakemod.Thing(1)`},
		{"NEWOBJ of a set", "c__builtin__\nset\n)\x81(K\x01\x90.", `set(1)`},
		{"NEWOBJ of an OrderedDict", "ccollections\nOrderedDict\n)\x81K\x01K\x02s.", `{1: 2}`},

		// Known constructors.
		{"OrderedDict from pairs", "ccollections\nOrderedDict\n((K\x01K\x02l(K\x03K\x04tl\x85R.", `{1: 2, 3: 4}`},
		{"OrderedDict from a dict", "ccollections\nOrderedDict\n}K\x01K\x02s\x85R.", `{1: 2}`},
		{"defaultdict", "ccollections\ndefaultdict\nc__builtin__\nlist\n\x85R(K\x01K\x02u.", `{1: 2}`},
		{"list and tuple", "(c__builtin__\nlist\n(K\x01t\x85Rc__builtin__\ntuple\n]K\x02a\x85Rt.", `([1], (2,))`},
		{"bytes", "c__builtin__\nbytes\n)R.", `b""`},
		{"bytearray from ints", "c__builtin__\nbytearray\n(K\x01K\xffl\x85R.", `b"\x01\xff"`},
		{"bytearray from text", "c__builtin__\nbytearray\n(V\xff\nVlatin1\ntR.", `b"\xff"`},
		{"_codecs.encode UTF-8", "c_codecs\nencode\nV\\u5212\n\x85R.", `b"划"`},
		{"_codecs.encode Latin-1 out of range", "c_codecs\nencode\n(V\\u5212\nVlatin-1\ntR.", `b"?"`},
		{"torch parameter", "ctorch._utils\n_rebuild_parameter\nctorch._utils\n_rebuild_tensor_v2\n(P0\nK\x00(K\x10K\x03t" +
			"K\x01K\x01\x86\x89ccollections\nOrderedDict\n)RtR\x88ccollections\nOrderedDict\n)R\x87R.", `parameter[16 3]`},
		{"torch tensor of unknown shape", "ctorch._utils\n_rebuild_tensor_v2\n(P0\nK\x00Vbad\nK\x01tR.", `tensor[?]`},
		{"torch tensor with a negative size", "ctorch._utils\n_rebuild_tensor_v2\n(P0\nK\x00J\xff\xff\xff\xff\x85K\x01\x85tR.", `tensor[?]`},
		{"torch meta tensor", "ctorch._utils\n_rebuild_meta_tensor_no_storage\n(ctorch\nfloat16\n(K\x02K\x05ttR.", `tensor[2 5]`},
		{"torch sparse tensor", "ctorch._utils\n_rebuild_sparse_tensor\n)R.", `tensor[?]`},
		{"torch parameter of a non-tensor", "ctorch._utils\n_rebuild_parameter\n(K\x01\x88}tR.", `parameter[?]`},
		{"torch tensor subclass", "ctorch._tensor\n_rebuild_from_type_v2\n(ctorch._utils\n_rebuild_tensor_v2\n" +
			"ctorch.nn.parameter\nParameter\n(P0\nK\x00(K\x04t(K\x01t\x89}tNtR.", `parameter[4]`},
		{"torch tensor subclass of an unknown builder", "ctorch._tensor\n_rebuild_from_type_v2\n(cx\ny\nctorch\nTensor\n)NtR.",
			`tensor[?]`},
		{"torch.Size", "ctorch\nSize\n(K\x02K\x03t\x85R.", `(2, 3)`},
		{"numpy scalar", "cnumpy.core.multiarray\nscalar\ncnumpy\ndtype\n(Vf8\nI00\nI01\ntR(K\x03V<\nNNNJ\xff\xff\xff\xff" +
			"J\xff\xff\xff\xffK\x00tbC\x08\x00\x00\x00\x00\x00\x00\xd0?\x86R.", `0.25`},
		{"numpy scalar, big-endian", "cnumpy._core.multiarray\nscalar\ncnumpy\ndtype\n(Vf8\nI00\nI01\ntR(K\x03V>\nNNN" +
			"J\xff\xff\xff\xffJ\xff\xff\xff\xffK\x00tbC\x08?\xd0\x00\x00\x00\x00\x00\x00\x86R.", `0.25`},
		{"numpy scalar from text", "cnumpy.core.multiarray\nscalar\ncnumpy\ndtype\n(Vi2\nI00\nI01\ntRV\xfe\xff\n\x86R.", `-2`},
		{"numpy half and integers", "(cnumpy\ndtype\n(Vf2\nI00\nI01\ntRp0\n0cnumpy.core.multiarray\nscalar\np1\n" +
			"g0\nC\x02\x00\xbc\x86Rg1\ncnumpy\ndtype\n(Vu8\nI00\nI01\ntRC\x08\xff\xff\xff\xff\xff\xff\xff\xff\x86R" +
			"g1\ncnumpy\ndtype\n(Vi1\nI00\nI01\ntRC\x01\xff\x86Rg1\ncnumpy\ndtype\n(V?\nI00\nI01\ntRC\x01\x01\x86Rt.",
			`(-1, <bigint>, -1, True)`},
		{"numpy scalar of another dtype", "cnumpy.core.multiarray\nscalar\ncnumpy\ndtype\n(Vc8\nI00\nI01\ntRC\x01\x00\x86R.",
			`numpy.core.multiarray.scalar(numpy.dtype("c8", False, True), b"\x00")`},
		{"PosixPath", "cpathlib\nPosixPath\n(V/a\nVb\nV/c\nVd.yaml\ntR.", `"/c/d.yaml"`},
		{"PurePosixPath, Python 3.13", "cpathlib._local\nPurePosixPath\n(Vruns/\nVbest.pt\ntR.", `"runs/best.pt"`},
		{"PureWindowsPath", "cpathlib\nPureWindowsPath\n(VC:\\u005c\nVx\nVy.pt\ntR.", `"C:\\x\\y.pt"`},
		{"pathlib with a non-string part", "cpathlib\nPath\n(K\x01tR.", `pathlib.Path(1)`},
		{"os.system is never called", "cos\nsystem\nVecho hi\n\x85R.", `os.system("echo hi")`},
		{"REDUCE of a non-class", "}K\x01\x85R.", `?(1)`},

		// BUILD and container operations on objects.
		{"BUILD with a non-dict state", "c__main__\nP\n)\x81K\x05b.", `__main__.P()<5>`},
		{"BUILD on a dict is ignored", "}}b.", `{}`},
		{"BUILD merges", "c__main__\nP\n)\x81(Va\nK\x01db(Vb\nK\x02db(Va\nK\x03db.", `__main__.P(){"a": 3, "b": 2}`},
		{"BUILD with slots", "c__main__\nP\n)\x81N(Va\nK\x01d\x86b.", `__main__.P(){"a": 1}`},
		{"SETITEMS on an object", "c__main__\nD\n)\x81(K\x01K\x02u.", `__main__.D()dict{1: 2}`},
		{"APPENDS on an object", "c__main__\nL\n)\x81(K\x01e.", `__main__.L()[1]`},
		{"ADDITEMS on an object", "c__main__\nS\n)\x81(K\x01\x90.", `__main__.S()[1]`},

		// Dict keys equal in Python share one entry.
		{"equal keys", "}(K\x01Va\nG?\xf0\x00\x00\x00\x00\x00\x00Vb\n\x88Vc\nK\x02Vd\nu.", `{1: "c", 2: "d"}`},
		{"tuple keys", "}((K\x01K\x02tVa\n(K\x01K\x02tVb\nu.", `{(1, 2): "b"}`},
		{"unhashable keys", "}(]Va\n]Vb\nu.", `{[]: "a", []: "b"}`},
		{"a list containing itself", "]q\x00h\x00a.", `[<cycle>]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := repr(mustUnpickle(t, tt.data)); got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestUnpickleErrors(t *testing.T) {
	tests := []struct {
		name, data, want string
	}{
		{"empty", "", "缺少 STOP"},
		{"no STOP", "N", "缺少 STOP"},
		{"unknown opcode", "\xff", "未知的操作码 0xff"},
		{"POP without anything", "0.", "缺少 MARK"},
		{"APPEND on an empty stack", "a.", "栈为空"},
		{"APPEND to an int", "K\x01Na.", "不能向 int 追加元素"},
		{"SETITEM below the MARK", "}(K\x01K\x02s.", "栈为空"},
		{"odd SETITEMS", "}(K\x01u.", "不成对"},
		{"odd DICT", "(K\x01d.", "不成对"},
		{"SETITEM on a list", "]K\x01K\x02s.", "不能向 list 写入键值"},
		{"missing memo entry", "h\x05.", "不存在的 memo 项 5"},
		{"negative PUT", "Np-1\n.", "memo 编号为负数"},
		{"bad PUT", "Npx\n.", "PUT 的参数不是整数"},
		{"bad GET", "gx\n.", "GET 的参数不是整数"},
		{"unknown protocol", "\x80\x06.", "不支持的 pickle 协议 6"},
		{"EXT1", "\x82\x01.", "EXT"},
		{"STACK_GLOBAL of ints", "K\x01K\x02\x93.", "STACK_GLOBAL"},
		{"negative BINSTRING length", "T\xff\xff\xff\xff.", "长度为负数"},
		{"negative LONG4 length", "\x8b\xff\xff\xff\xff.", "长度为负数"},
		{"huge LONG4", "\x8b\xff\xff\xff\x7f.", "声明的长度为 2147483647 字节"},
		{"huge BINUNICODE", "X\xff\xff\xff\xffabc.", "声明的长度为 4294967295 字节"},
		{"huge BINUNICODE8", "\x8d\xff\xff\xff\xff\xff\xff\xff\xff.", "数据不完整"},
		{"huge BINBYTES8", "\x8e\x00\x00\x00\x00\x00\x00\x00\x80.", "数据不完整"},
		{"huge BYTEARRAY8", "\x96\x00\x00\x00\x00\x01\x00\x00\x00.", "数据不完整"},
		{"truncated argument", "K", "需要 1 字节，只剩 0 字节"},
		{"truncated line", "cmodule", "缺少换行符"},
		{"NEXT_BUFFER", "\x97.", "NEXT_BUFFER"},
		{"READONLY_BUFFER on an empty stack", "\x98.", "栈为空"},
		{"REDUCE of an int", "c__main__\nf\nK\x01R.", "REDUCE 的参数不是元组（int）"},
		{"NEWOBJ of an int", "c__main__\nf\nK\x01\x81.", "NEWOBJ 的参数不是元组（int）"},
		{"empty OBJ", "(o.", "OBJ 缺少类"},
		{"unquoted STRING", "S'abc\n.", "缺少引号"},
		{"bad INT", "Iabc\n.", "整数格式不正确"},
		{"bad FLOAT", "Fxyz\n.", "FLOAT 的参数不是数字"},
		{"TUPLE2 across a MARK", "K\x01(K\x02\x86.", "栈中的元素不足 2 个"},
		{"ADDITEMS to a frozenset", "(K\x01\x91(K\x02\x90.", "不能向 frozenset 添加元素"},
		{"ADDITEMS to an int", "K\x01(K\x02\x90.", "不能向 int 添加元素"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := unpickle([]byte(tt.data), defaultLimits)
			var pe *pickleError
			if !errors.As(err, &pe) || pe.tooLarge {
				t.Fatalf("err = %v, want a corrupt pickle", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %q, want %q", err, tt.want)
			}
		})
	}
}

func TestUnpickleLimits(t *testing.T) {
	small := limits{ops: 100, values: 100, stack: 10, marks: 3, memo: 2}
	tests := []struct {
		name, data, want string
	}{
		{"ops", strings.Repeat("N0", 51) + "N.", "操作数超过上限"},
		{"values", "]" + strings.Repeat("Na", 60) + ".", "对象数量超过上限"},
		{"container values", strings.Repeat("]0", 30) + "N.", "对象数量超过上限"},
		// Merging the same memoized dict into an object again and again.
		{"BUILD merges", "c__main__\nP\n)\x81}b(Va\nK\x01Vb\nK\x02Vc\nK\x03dq\x00b" + strings.Repeat("h\x00b", 40) + ".",
			"对象数量超过上限"},
		{"stack", strings.Repeat("N", 11) + ".", "栈深度超过上限"},
		{"marks", "((((.", "嵌套层数超过上限"},
		{"memo", "Nq\x00q\x01q\x02.", "memo 项数超过上限"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := unpickle([]byte(tt.data), small)
			var pe *pickleError
			if !errors.As(err, &pe) || !pe.tooLarge || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
	// Overwriting a memo entry does not count again.
	if _, err := unpickle([]byte("Nq\x00q\x00q\x00q\x01q\x01."), small); err != nil {
		t.Errorf("memo overwrite: %v", err)
	}
}

// TestUnpickleCraftedDefaults runs crafted pickles against the default
// limits: each must fail fast (or work) without exhausting memory.
func TestUnpickleCraftedDefaults(t *testing.T) {
	var memo bytes.Buffer
	memo.WriteString("N")
	for i := 0; i <= defaultLimits.memo; i++ {
		memo.WriteByte(opLongBinPut)
		_ = binary.Write(&memo, binary.LittleEndian, uint32(i))
	}
	memo.WriteString(".")

	// APPENDS of 1000 memo references, repeated: each round adds 1000
	// items without growing the input much.
	var amplify bytes.Buffer
	amplify.WriteString("]q\x00]")
	round := "(" + strings.Repeat("h\x00", 1000) + "e"
	for amplify.Len() < 8<<20 {
		amplify.WriteString(round)
	}
	amplify.WriteString(".")

	tests := []struct {
		name string
		data []byte
		want string // "" when the pickle is fine
	}{
		{"deep MARK nesting", bytes.Repeat([]byte("("), 1<<20), "嵌套层数超过上限"},
		{"deep stack", bytes.Repeat([]byte("]"), 1<<20), "栈深度超过上限"},
		{"memo abuse", memo.Bytes(), "memo 项数超过上限"},
		{"amplification through the memo", amplify.Bytes(), "对象数量超过上限"},
		{"tuples nested 250000 levels deep", []byte(")" + strings.Repeat("\x85", 250000) + "."), ""},
		{"lists nested through the memo", []byte("]q\x00" + strings.Repeat("]q\x01h\x00h\x01aq\x00\x30", 100000) + "h\x00."), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := unpickle(tt.data, defaultLimits)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				// Rendering or hashing the result must not recurse without bound.
				if _, ok := hashKey(v); ok && strings.HasPrefix(tt.name, "tuples") {
					t.Error("a deeply nested tuple was hashed")
				}
				return
			}
			var pe *pickleError
			if !errors.As(err, &pe) || !pe.tooLarge || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestDictKeys(t *testing.T) {
	d := &pyDict{}
	for i := 0; i < 20; i++ {
		d.set(int64(i), i)
	}
	d.set(true, "one") // True == 1
	d.set(float64(2), "two")
	d.set("x", 1)
	d.set(&pyList{}, "unhashable")
	d.set(&pyList{}, "unhashable")
	if d.len() != 23 {
		t.Fatalf("len = %d, want 23", d.len())
	}
	if d.keys[1] != int64(1) || d.vals[1] != "one" || d.vals[2] != "two" {
		t.Errorf("replaced entries: %v %v", d.keys[1], d.vals[1:3])
	}
	if v, ok := d.get("x"); !ok || v != 1 {
		t.Errorf("get(x) = %v, %v", v, ok)
	}
	if _, ok := d.get("missing"); ok {
		t.Error("get(missing) found an entry")
	}
	var nilDict *pyDict
	if _, ok := nilDict.get("x"); ok || nilDict.len() != 0 {
		t.Error("nil dict")
	}

	// Tuple keys: hashed up to a bounded depth and size.
	deep := &pyTuple{items: []any{int64(1)}}
	for i := 0; i < 10; i++ {
		deep = &pyTuple{items: []any{deep}}
	}
	if _, ok := hashKey(deep); ok {
		t.Error("deep tuple hashed")
	}
	wide := &pyTuple{items: make([]any, 100)}
	if _, ok := hashKey(wide); ok {
		t.Error("wide tuple hashed")
	}
	a, ok1 := hashKey(&pyTuple{items: []any{"a", int64(1), nil, pyBytes("b"), 1.5}})
	b, ok2 := hashKey(&pyTuple{items: []any{"a", true, nil, pyBytes("b"), 1.5}})
	if !ok1 || !ok2 || a != b {
		t.Errorf("equal tuples hash to %v and %v", a, b)
	}
	if k, ok := hashKey(math.NaN()); ok {
		t.Errorf("NaN hashed to %v", k)
	}
	if _, ok := hashKey(&pyTuple{items: []any{&pyObject{}}}); ok {
		t.Error("tuple of an object hashed")
	}
}

func TestHalfToFloat(t *testing.T) {
	for h, want := range map[uint16]float64{
		0x3c00: 1, 0xc000: -2, 0x3800: 0.5, 0x0001: math.Ldexp(1, -24), 0x7bff: 65504, 0x7c00: math.Inf(1), 0xfc00: math.Inf(-1),
	} {
		if got := halfToFloat(h); got != want {
			t.Errorf("halfToFloat(%#x) = %v, want %v", h, got, want)
		}
	}
	if !math.IsNaN(halfToFloat(0x7e00)) {
		t.Error("halfToFloat(0x7e00) is not NaN")
	}
}
