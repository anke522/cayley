package graphtest

import (
	"bytes"
	"context"
	"math"
	"math/rand"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/cayleygraph/cayley/graph"
	"github.com/cayleygraph/cayley/graph/graphtest/testutil"
	"github.com/cayleygraph/cayley/graph/iterator"
	"github.com/cayleygraph/cayley/graph/iterator/giterator"
	"github.com/cayleygraph/cayley/graph/path/pathtest"
	"github.com/cayleygraph/cayley/graph/values"
	"github.com/cayleygraph/cayley/quad"
	"github.com/cayleygraph/cayley/query"
	"github.com/cayleygraph/cayley/query/shape"
	"github.com/cayleygraph/cayley/query/shape/gshape"
	"github.com/cayleygraph/cayley/schema"
	"github.com/cayleygraph/cayley/writer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type Config = testutil.Config

var graphTests = []struct {
	name string
	test func(t testing.TB, gen testutil.Database)
}{
	{"load one quad", TestLoadOneQuad},
	{"delete quad", TestDeleteQuad},
	{"sizes", TestSizes},
	{"iterator", TestIterator},
	{"hasa", TestHasA},
	{"set iterator", TestSetIterator},
	{"deleted from iterator", TestDeletedFromIterator},
	{"load typed quad", TestLoadTypedQuads},
	{"add and remove", TestAddRemove},
	{"node delete", TestNodeDelete},
	{"iterators and next result order", TestIteratorsAndNextResultOrderA},
	{"compare typed values", TestCompareTypedValues},
	{"schema", TestSchema},
}

func TestAll(t *testing.T, gen testutil.Database) {
	for _, gt := range graphTests {
		t.Run(gt.name, func(t *testing.T) {
			gt.test(t, gen)
		})
	}
	t.Run("writers", func(t *testing.T) {
		TestWriters(t, gen)
	})
	t.Run("1k", func(t *testing.T) {
		Test1K(t, gen)
	})
	t.Run("paths", func(t *testing.T) {
		pathtest.RunTestMorphisms(t, &gen)
	})
	t.Run("integration", func(t *testing.T) {
		TestIntegration(t, gen)
	})
	t.Run("concurrent", func(t *testing.T) {
		if testing.Short() {
			t.SkipNow()
		}
		t.SkipNow() // FIXME: switch with a flag
		testConcurrent(t, gen)
	})
}

func BenchmarkAll(t *testing.B, gen testutil.Database) {
	t.Run("integration", func(t *testing.B) {
		BenchmarkIntegration(t, gen, gen.Config.AlwaysRunIntegration)
	})
}

// This is a simple test graph.
//
//    +---+                        +---+
//    | A |-------               ->| F |<--
//    +---+       \------>+---+-/  +---+   \--+---+
//                 ------>|#B#|      |        | E |
//    +---+-------/      >+---+      |        +---+
//    | C |             /            v
//    +---+           -/           +---+
//      ----    +---+/             |#G#|
//          \-->|#D#|------------->+---+
//              +---+
//
func MakeQuadSet() []quad.Quad {
	return []quad.Quad{
		quad.Make("A", "follows", "B", nil),
		quad.Make("C", "follows", "B", nil),
		quad.Make("C", "follows", "D", nil),
		quad.Make("D", "follows", "B", nil),
		quad.Make("B", "follows", "F", nil),
		quad.Make("F", "follows", "G", nil),
		quad.Make("D", "follows", "G", nil),
		quad.Make("E", "follows", "F", nil),
		quad.Make("B", "status", "cool", "status_graph"),
		quad.Make("D", "status", "cool", "status_graph"),
		quad.Make("G", "status", "cool", "status_graph"),
	}
}

func IteratedQuads(t testing.TB, qs graph.QuadStore, it iterator.Iterator) []quad.Quad {
	ctx := context.TODO()
	var res quad.ByQuadString
	for it.Next(ctx) {
		res = append(res, qs.Quad(it.Result()))
	}
	require.Nil(t, it.Err())
	sort.Sort(res)
	if res == nil {
		return []quad.Quad(nil) // GopherJS seems to have a bug with this type conversion for a nil value
	}
	return res
}

func ExpectIteratedQuads(t testing.TB, qs graph.QuadStore, it iterator.Iterator, exp []quad.Quad, sortQuads bool) {
	got := IteratedQuads(t, qs, it)
	if sortQuads {
		sort.Sort(quad.ByQuadString(exp))
		sort.Sort(quad.ByQuadString(got))
	}
	if len(exp) == 0 {
		exp = nil // GopherJS seems to have a bug with nil value
	}
	require.Equal(t, exp, got)
}

func ExpectIteratedRawStrings(t testing.TB, qs graph.QuadStore, it iterator.Iterator, exp []string) {
	//sort.Strings(exp)
	got := IteratedStrings(t, qs, it)
	//sort.Strings(got)
	require.Equal(t, exp, got)
}

func ExpectIteratedValues(t testing.TB, qs graph.QuadStore, it iterator.Iterator, exp []quad.Value, sortVals bool) {
	//sort.Strings(exp)
	got := IteratedValues(t, qs, it)
	//sort.Strings(got)
	if sortVals {
		exp = append([]quad.Value{}, exp...)
		sort.Sort(quad.ByValueString(exp))
	}

	require.Equal(t, len(exp), len(got), "%v\nvs\n%v", exp, got)
	for i := range exp {
		if eq, ok := exp[i].(quad.Equaler); ok {
			require.True(t, eq.Equal(got[i]))
		} else {
			require.True(t, exp[i] == got[i], "%v\nvs\n%v\n\n%v\nvs\n%v", exp[i], got[i], exp, got)
		}
	}
}

func iteratedValues(t testing.TB, qs graph.QuadStore, it iterator.Iterator) []quad.Value {
	ctx := context.TODO()
	var res []quad.Value
	for it.Next(ctx) {
		qv, _ := graph.ValueOf(ctx, qs, it.Result())
		res = append(res, qv)
	}
	require.Nil(t, it.Err())
	return res
}

func IteratedStrings(t testing.TB, qs graph.QuadStore, it iterator.Iterator) []string {
	res := iteratedValues(t, qs, it)
	if len(res) == 0 {
		return nil
	}
	out := make([]string, 0, len(res))
	for _, qv := range res {
		out = append(out, quad.ToString(qv))
	}
	sort.Strings(out)
	return out
}

func IteratedValues(t testing.TB, qs graph.QuadStore, it iterator.Iterator) []quad.Value {
	res := iteratedValues(t, qs, it)
	sort.Sort(quad.ByValueString(res))
	return res
}

func TestLoadOneQuad(t testing.TB, gen testutil.Database) {
	c := gen.Config
	qs, opts, closer := gen.Run(t)
	defer closer()

	w := testutil.MakeWriter(t, qs, opts)

	q := quad.Make(
		"Something",
		"points_to",
		"Something Else",
		"context",
	)

	err := w.AddQuad(q)
	require.NoError(t, err)

	ctx := context.TODO()

	for _, d := range quad.Directions {
		pq := q.Get(d)
		tok, err := graph.RefOf(ctx, qs, pq)
		require.NoError(t, err)
		require.NotNil(t, tok, "quad store failed to find value: %q", pq)

		val, err := graph.ValueOf(ctx, qs, tok)
		require.NoError(t, err)
		require.NotNil(t, val, "quad store failed to decode value: %q", pq)
		require.Equal(t, pq, val, "quad store failed to roundtrip value: %q", pq)
	}
	exp := int64(5)
	if c.NoPrimitives {
		exp = 1
	}
	require.Equal(t, exp, qs.Stats().Links, "Unexpected quadstore size")

	ExpectIteratedQuads(t, qs, qs.AllQuads().BuildIterator(), []quad.Quad{q}, false)

	for _, d := range quad.Directions {
		pq := q.Get(d)
		tok, err := graph.RefOf(ctx, qs, pq)
		require.NoError(t, err)
		ExpectIteratedQuads(t, qs, qs.QuadIterator(d, tok).BuildIterator(), []quad.Quad{q}, false)
	}
}

func TestWriters(t *testing.T, gen testutil.Database) {
	for _, mis := range []bool{false, true} {
		for _, dup := range []bool{false, true} {
			name := []byte("__")
			if dup {
				name[0] = 'd'
			}
			if mis {
				name[1] = 'm'
			}
			t.Run(string(name), func(t *testing.T) {
				qs, _, closer := gen.Run(t)
				defer closer()

				w, err := writer.NewSingle(qs, graph.IgnoreOpts{
					IgnoreDup: dup, IgnoreMissing: mis,
				})
				require.NoError(t, err)

				quads := func(arr ...quad.Quad) {
					ExpectIteratedQuads(t, qs, qs.AllQuads().BuildIterator(), arr, false)
				}

				deltaErr := func(exp, err error) {
					if exp == graph.ErrQuadNotExist && mis {
						require.NoError(t, err)
						return
					} else if exp == graph.ErrQuadExists && dup {
						require.NoError(t, err)
						return
					}
					e, ok := err.(*graph.DeltaError)
					require.True(t, ok, "expected delta error, got: %T (%v)", err, err)
					require.Equal(t, exp, e.Err)
				}

				// add one quad
				q := quad.Make("a", "b", "c", nil)
				err = w.AddQuad(q)
				require.NoError(t, err)
				quads(q)

				// try to add the same quad again
				err = w.AddQuad(q)
				deltaErr(graph.ErrQuadExists, err)
				quads(q)

				// remove quad with non-existent node
				err = w.RemoveQuad(quad.Make("a", "b", "not-existent", nil))
				deltaErr(graph.ErrQuadNotExist, err)

				// remove non-existent quads
				err = w.RemoveQuad(quad.Make("a", "c", "b", nil))
				deltaErr(graph.ErrQuadNotExist, err)
				err = w.RemoveQuad(quad.Make("c", "b", "a", nil))
				deltaErr(graph.ErrQuadNotExist, err)

				// make sure store is still in correct state
				quads(q)

				// remove existing quad
				err = w.RemoveQuad(q)
				require.NoError(t, err)
				quads()

				// add the same quad again
				err = w.AddQuad(q)
				require.NoError(t, err)
				quads(q)
			})
		}
	}
}

func Test1K(t *testing.T, gen testutil.Database) {
	c := gen.Config
	qs, _, closer := gen.Run(t)
	defer closer()

	pg := c.PageSize
	if pg == 0 {
		pg = 100
	}
	n := pg*3 + 1

	w, err := writer.NewSingle(qs, graph.IgnoreOpts{})
	require.NoError(t, err)

	qw := graph.NewWriter(w)
	exp := make([]quad.Quad, 0, n)
	for i := 0; i < n; i++ {
		q := quad.Make(i, i, i, nil)
		exp = append(exp, q)
		qw.WriteQuad(q)
	}
	err = qw.Flush()
	require.NoError(t, err)

	ExpectIteratedQuads(t, qs, qs.AllQuads().BuildIterator(), exp, true)
}

func TestSizes(t testing.TB, gen testutil.Database) {
	conf := gen.Config
	qs, opts, closer := gen.Run(t)
	defer closer()

	w := testutil.MakeWriter(t, qs, opts)

	err := w.AddQuadSet(MakeQuadSet())
	require.NoError(t, err)
	exp := int64(22)
	if conf.NoPrimitives {
		exp = 11
	}
	require.Equal(t, exp, qs.Stats().Links, "Unexpected quadstore size")

	err = w.RemoveQuad(quad.Make(
		"A",
		"follows",
		"B",
		nil,
	))
	require.NoError(t, err)
	err = w.RemoveQuad(quad.Make(
		"A",
		"follows",
		"B",
		nil,
	))
	require.True(t, graph.IsQuadNotExist(err))
	if !conf.SkipSizeCheckAfterDelete {
		exp = int64(20)
		if conf.NoPrimitives {
			exp = 10
		}
		require.Equal(t, exp, qs.Stats().Links, "Unexpected quadstore size after RemoveQuad")
	} else {
		require.Equal(t, int64(11), qs.Stats().Links, "Unexpected quadstore size")
	}
}

func TestIterator(t testing.TB, gen testutil.Database) {
	ctx := context.TODO()
	qs, opts, closer := gen.Run(t)
	defer closer()

	testutil.MakeWriter(t, qs, opts, MakeQuadSet()...)

	var it iterator.Iterator

	it = qs.AllNodes().BuildIterator()
	require.NotNil(t, it)

	size, _ := it.Size()
	require.True(t, size > 0 && size < 23, "Unexpected size: %v", size)
	// TODO: leveldb had this test
	//if exact {
	//	t.Errorf("Got unexpected exact result.")
	//}

	expect := []string{
		"A",
		"B",
		"C",
		"D",
		"E",
		"F",
		"G",
		"follows",
		"status",
		"cool",
		"status_graph",
	}
	sort.Strings(expect)
	for i := 0; i < 2; i++ {
		got := IteratedStrings(t, qs, it)
		sort.Strings(got)
		require.Equal(t, expect, got, "Unexpected iterated result on repeat %d", i)
		it.Reset()
	}

	for _, pq := range expect {
		ref, _ := graph.RefOf(ctx, qs, quad.Raw(pq))
		ok := it.Contains(ctx, ref)
		require.NoError(t, it.Err())
		require.True(t, ok, "Failed to find and check %q correctly", pq)

	}
	// FIXME(kortschak) Why does this fail?
	/*
		for _, pq := range []string{"baller"} {
			if it.Contains(qs.ValueOf(pq)) {
				t.Errorf("Failed to check %q correctly", pq)
			}
		}
	*/
	it.Reset()

	it = qs.AllQuads().BuildIterator()
	require.True(t, it.Next(ctx))

	q := qs.Quad(it.Result())
	require.Nil(t, it.Err())
	require.True(t, q.IsValid(), "Invalid quad returned: %q", q)
	set := MakeQuadSet()
	var ok bool
	for _, e := range set {
		if e.String() == q.String() {
			ok = true
			break
		}
	}
	require.True(t, ok, "Failed to find %q during iteration, got:%q", q, set)
}

func TestHasA(t testing.TB, gen testutil.Database) {
	qs, opts, closer := gen.Run(t)
	defer closer()

	testutil.MakeWriter(t, qs, opts, MakeQuadSet()...)

	var it iterator.Iterator = giterator.NewHasA(qs,
		giterator.NewLinksTo(qs, qs.AllNodes().BuildIterator(), quad.Predicate),
		quad.Predicate)
	defer it.Close()

	var exp []quad.Value
	for i := 0; i < 8; i++ {
		exp = append(exp, quad.Raw("follows"))
	}
	for i := 0; i < 3; i++ {
		exp = append(exp, quad.Raw("status"))
	}
	ExpectIteratedValues(t, qs, it, exp, false)
}

func TestSetIterator(t testing.TB, gen testutil.Database) {
	qs, opts, closer := gen.Run(t)
	defer closer()

	testutil.MakeWriter(t, qs, opts, MakeQuadSet()...)

	expectIteratedQuads := func(it iterator.Iterator, exp []quad.Quad) {
		ExpectIteratedQuads(t, qs, it, exp, false)
	}

	quadIter := func(d quad.Direction, v string) iterator.Iterator {
		qv, _ := graph.RefOf(context.TODO(), qs, quad.String(v))
		return qs.QuadIterator(d, qv).BuildIterator()
	}

	// Subject iterator.
	it := quadIter(quad.Subject, "C")

	expectIteratedQuads(it, []quad.Quad{
		quad.Make("C", "follows", "B", nil),
		quad.Make("C", "follows", "D", nil),
	})
	it.Reset()

	and := iterator.NewAnd(
		qs.AllQuads().BuildIterator(),
		it,
	)

	expectIteratedQuads(and, []quad.Quad{
		quad.Make("C", "follows", "B", nil),
		quad.Make("C", "follows", "D", nil),
	})

	// Object iterator.
	it = quadIter(quad.Object, "F")

	expectIteratedQuads(it, []quad.Quad{
		quad.Make("B", "follows", "F", nil),
		quad.Make("E", "follows", "F", nil),
	})

	and = iterator.NewAnd(
		quadIter(quad.Subject, "B"),
		it,
	)

	expectIteratedQuads(and, []quad.Quad{
		quad.Make("B", "follows", "F", nil),
	})

	// Predicate iterator.
	it = quadIter(quad.Predicate, "status")

	expectIteratedQuads(it, []quad.Quad{
		quad.Make("B", "status", "cool", "status_graph"),
		quad.Make("D", "status", "cool", "status_graph"),
		quad.Make("G", "status", "cool", "status_graph"),
	})

	// Label iterator.
	it = quadIter(quad.Label, "status_graph")

	expectIteratedQuads(it, []quad.Quad{
		quad.Make("B", "status", "cool", "status_graph"),
		quad.Make("D", "status", "cool", "status_graph"),
		quad.Make("G", "status", "cool", "status_graph"),
	})
	it.Reset()

	// Order is important
	and = iterator.NewAnd(
		quadIter(quad.Subject, "B"),
		it,
	)

	expectIteratedQuads(and, []quad.Quad{
		quad.Make("B", "status", "cool", "status_graph"),
	})
	it.Reset()

	// Order is important
	and = iterator.NewAnd(
		it,
		quadIter(quad.Subject, "B"),
	)

	expectIteratedQuads(and, []quad.Quad{
		quad.Make("B", "status", "cool", "status_graph"),
	})
}

func TestDeleteQuad(t testing.TB, gen testutil.Database) {
	qs, opts, closer := gen.Run(t)
	defer closer()

	w := testutil.MakeWriter(t, qs, opts, MakeQuadSet()...)

	refOf := func(v string) values.Ref {
		vn, _ := graph.RefOf(context.TODO(), qs, quad.Raw(v))
		return vn
	}

	vn := refOf("E")
	require.NotNil(t, vn)

	it := qs.QuadIterator(quad.Subject, vn).BuildIterator()
	ExpectIteratedQuads(t, qs, it, []quad.Quad{
		quad.Make("E", "follows", "F", nil),
	}, false)
	it.Close()

	err := w.RemoveQuad(quad.Make("E", "follows", "F", nil))
	require.NoError(t, err)

	vn2 := refOf("E")
	it = qs.QuadIterator(quad.Subject, vn2).BuildIterator()
	ExpectIteratedQuads(t, qs, it, nil, false)
	it.Close()

	it = qs.AllQuads().BuildIterator()
	ExpectIteratedQuads(t, qs, it, []quad.Quad{
		quad.Make("A", "follows", "B", nil),
		quad.Make("C", "follows", "B", nil),
		quad.Make("C", "follows", "D", nil),
		quad.Make("D", "follows", "B", nil),
		quad.Make("B", "follows", "F", nil),
		quad.Make("F", "follows", "G", nil),
		quad.Make("D", "follows", "G", nil),
		quad.Make("B", "status", "cool", "status_graph"),
		quad.Make("D", "status", "cool", "status_graph"),
		quad.Make("G", "status", "cool", "status_graph"),
	}, true)
	it.Close()
}

func TestDeletedFromIterator(t testing.TB, gen testutil.Database) {
	conf := gen.Config
	qs, opts, closer := gen.Run(t)
	defer closer()
	if conf.SkipDeletedFromIterator {
		t.SkipNow()
	}

	w := testutil.MakeWriter(t, qs, opts, MakeQuadSet()...)

	// Subject iterator.
	vn, _ := graph.RefOf(context.TODO(), qs, quad.Raw("E"))
	it := qs.QuadIterator(quad.Subject, vn).BuildIterator()

	ExpectIteratedQuads(t, qs, it, []quad.Quad{
		quad.Make("E", "follows", "F", nil),
	}, false)

	it.Reset()

	w.RemoveQuad(quad.Make("E", "follows", "F", nil))

	ExpectIteratedQuads(t, qs, it, nil, false)
}

func TestLoadTypedQuads(t testing.TB, gen testutil.Database) {
	conf := gen.Config
	qs, opts, closer := gen.Run(t)
	defer closer()

	w := testutil.MakeWriter(t, qs, opts)

	values := []quad.Value{
		quad.BNode("A"), quad.IRI("name"), quad.String("B"), quad.IRI("graph"),
		quad.IRI("B"), quad.Raw("<type>"),
		quad.TypedString{Value: "10", Type: "int"},
		quad.LangString{Value: "value", Lang: "en"},
		quad.Int(-123456789),
		quad.Float(-12345e-6),
		quad.Bool(true),
		quad.Time(time.Now()),
	}

	err := w.AddQuadSet([]quad.Quad{
		{values[0], values[1], values[2], values[3]},
		{values[4], values[5], values[6], nil},
		{values[4], values[5], values[7], nil},
		{values[0], values[1], values[8], nil},
		{values[0], values[1], values[9], nil},
		{values[0], values[1], values[10], nil},
		{values[0], values[1], values[11], nil},
	})
	require.NoError(t, err)

	ctx := context.TODO()
	roundtrip := func(qv quad.Value) (quad.Value, error) {
		ref, err := graph.RefOf(ctx, qs, qv)
		if err != nil {
			return nil, err
		}
		return graph.ValueOf(ctx, qs, ref)
	}

	for _, pq := range values {
		got, err := roundtrip(pq)
		require.NoError(t, err)
		if !conf.UnTyped {
			if pt, ok := pq.(quad.Time); ok {
				var trim int64
				if conf.TimeInMcs {
					trim = 1000
				} else if conf.TimeInMs {
					trim = 1000000
				}
				if trim > 0 {
					tm := time.Time(pt)
					seconds := tm.Unix()
					nanos := int64(tm.Sub(time.Unix(seconds, 0)))
					if conf.TimeRound {
						nanos = (nanos/trim + ((nanos/(trim/10))%10)/5) * trim
					} else {
						nanos = (nanos / trim) * trim
					}
					pq = quad.Time(time.Unix(seconds, nanos).UTC())
				}
			}
			if eq, ok := pq.(quad.Equaler); ok {
				assert.True(t, eq.Equal(got), "Failed to roundtrip %q (%T), got %q (%T)", pq, pq, got, got)
			} else {
				assert.Equal(t, pq, got, "Failed to roundtrip %q (%T)", pq, pq)
			}
			// check if we can get received value again (hash roundtrip)
			got2, err := roundtrip(got)
			require.NoError(t, err)
			assert.Equal(t, got, got2, "Failed to use returned value to get it again")
		} else {
			assert.Equal(t, quad.StringOf(pq), quad.StringOf(got), "Failed to roundtrip raw %q (%T)", pq, pq)
		}
	}
	exp := int64(19)
	if conf.NoPrimitives {
		exp = 7
	}
	require.Equal(t, exp, qs.Stats().Links, "Unexpected quadstore size")
}

// TODO(dennwc): add tests to verify that QS behaves in a right way with IgnoreOptions,
// returns ErrQuadExists, ErrQuadNotExists is doing rollback.
func TestAddRemove(t testing.TB, gen testutil.Database) {
	conf := gen.Config
	qs, opts, closer := gen.Run(t)
	defer closer()

	if opts == nil {
		opts = make(graph.Options)
	}
	opts["ignore_duplicate"] = true

	w := testutil.MakeWriter(t, qs, opts, MakeQuadSet()...)

	sz := int64(22)
	if conf.NoPrimitives {
		sz = 11
	}
	require.Equal(t, sz, qs.Stats().Links, "Incorrect number of quads")

	all := qs.AllNodes().BuildIterator()
	expect := []string{
		"A",
		"B",
		"C",
		"D",
		"E",
		"F",
		"G",
		"cool",
		"follows",
		"status",
		"status_graph",
	}
	ExpectIteratedRawStrings(t, qs, all, expect)

	// Add more quads, some conflicts
	err := w.AddQuadSet([]quad.Quad{
		quad.Make("A", "follows", "B", nil), // duplicate
		quad.Make("F", "follows", "B", nil),
		quad.Make("C", "follows", "D", nil), // duplicate
		quad.Make("X", "follows", "B", nil),
	})
	assert.Nil(t, err, "AddQuadSet failed")

	sz = int64(25)
	if conf.NoPrimitives {
		sz = 13
	}
	assert.Equal(t, sz, qs.Stats().Links, "Incorrect number of quads")

	all = qs.AllNodes().BuildIterator()
	expect = []string{
		"A",
		"B",
		"C",
		"D",
		"E",
		"F",
		"G",
		"X",
		"cool",
		"follows",
		"status",
		"status_graph",
	}
	ExpectIteratedRawStrings(t, qs, all, expect)

	// Remove quad
	toRemove := quad.Make("X", "follows", "B", nil)
	err = w.RemoveQuad(toRemove)
	require.Nil(t, err, "RemoveQuad failed")
	err = w.RemoveQuad(toRemove)
	require.True(t, graph.IsQuadNotExist(err), "expected not exists error, got: %v", err)

	expect = []string{
		"A",
		"B",
		"C",
		"D",
		"E",
		"F",
		"G",
		"cool",
		"follows",
		"status",
		"status_graph",
	}
	ExpectIteratedRawStrings(t, qs, all, nil)
	all = qs.AllNodes().BuildIterator()
	ExpectIteratedRawStrings(t, qs, all, expect)
}

func TestIteratorsAndNextResultOrderA(t testing.TB, gen testutil.Database) {
	ctx := context.TODO()
	conf := gen.Config
	qs, opts, closer := gen.Run(t)
	defer closer()

	testutil.MakeWriter(t, qs, opts, MakeQuadSet()...)

	sz := int64(22)
	if conf.NoPrimitives {
		sz = 11
	}
	require.Equal(t, sz, qs.Stats().Links, "Incorrect number of quads")

	newFixed := func(v string) iterator.Iterator {
		ref, _ := graph.RefOf(ctx, qs, quad.Raw(v))
		return iterator.NewFixed(ref)
	}

	fixed := newFixed("C")
	fixed2 := newFixed("follows")

	all := qs.AllNodes().BuildIterator()

	innerAnd := iterator.NewAnd(
		giterator.NewLinksTo(qs, fixed2, quad.Predicate),
		giterator.NewLinksTo(qs, all, quad.Object),
	)

	hasa := giterator.NewHasA(qs, innerAnd, quad.Subject)
	outerAnd := iterator.NewAnd(fixed, hasa)

	require.True(t, outerAnd.Next(ctx), "Expected one matching subtree")

	nameOf := func(ref values.Ref) quad.Value {
		v, _ := graph.ValueOf(ctx, qs, ref)
		return v
	}

	val := outerAnd.Result()
	require.Equal(t, quad.Raw("C"), nameOf(val))

	var (
		got    []string
		expect = []string{"B", "D"}
	)
	for {
		got = append(got, quad.ToString(nameOf(all.Result())))
		if !outerAnd.NextPath(ctx) {
			break
		}
	}
	sort.Strings(got)

	require.Equal(t, expect, got)

	require.True(t, !outerAnd.Next(ctx), "More than one possible top level output?")
}

const lt, lte, gt, gte = shape.CompareLT, shape.CompareLTE, shape.CompareGT, shape.CompareGTE

var tzero = time.Unix(time.Now().Unix(), 0)

var casesCompare = []struct {
	op     shape.CmpOperator
	val    quad.Value
	expect []quad.Value
}{
	{lt, quad.BNode("b"), []quad.Value{
		quad.BNode("alice"),
	}},
	{lte, quad.BNode("bob"), []quad.Value{
		quad.BNode("alice"), quad.BNode("bob"),
	}},
	{lt, quad.String("b"), []quad.Value{
		quad.String("alice"),
	}},
	{lte, quad.String("bob"), []quad.Value{
		quad.String("alice"), quad.String("bob"),
	}},
	{gte, quad.String("b"), []quad.Value{
		quad.String("bob"), quad.String("charlie"), quad.String("dani"),
	}},
	{lt, quad.IRI("b"), []quad.Value{
		quad.IRI("alice"),
	}},
	{lte, quad.IRI("bob"), []quad.Value{
		quad.IRI("alice"), quad.IRI("bob"),
	}},
	{lte, quad.IRI("bob"), []quad.Value{
		quad.IRI("alice"), quad.IRI("bob"),
	}},
	{gte, quad.Int(111), []quad.Value{
		quad.Int(112), quad.Int(math.MaxInt64 - 1), quad.Int(math.MaxInt64),
	}},
	{gte, quad.Int(110), []quad.Value{
		quad.Int(110), quad.Int(112), quad.Int(math.MaxInt64 - 1), quad.Int(math.MaxInt64),
	}},
	{lt, quad.Int(20), []quad.Value{
		quad.Int(math.MinInt64 + 1), quad.Int(math.MinInt64),
	}},
	{lte, quad.Int(20), []quad.Value{
		quad.Int(math.MinInt64 + 1), quad.Int(math.MinInt64), quad.Int(20),
	}},
	{lte, quad.Time(tzero.Add(time.Hour)), []quad.Value{
		quad.Time(tzero), quad.Time(tzero.Add(time.Hour)),
	}},
	{gt, quad.Time(tzero.Add(time.Hour)), []quad.Value{
		quad.Time(tzero.Add(time.Hour * 49)), quad.Time(tzero.Add(time.Hour * 24 * 365)),
	}},
	// precision tests
	{gt, quad.Int(math.MaxInt64 - 1), []quad.Value{
		quad.Int(math.MaxInt64),
	}},
	{gte, quad.Int(math.MaxInt64 - 1), []quad.Value{
		quad.Int(math.MaxInt64 - 1), quad.Int(math.MaxInt64),
	}},
	{lt, quad.Int(math.MinInt64 + 1), []quad.Value{
		quad.Int(math.MinInt64),
	}},
	{lte, quad.Int(math.MinInt64 + 1), []quad.Value{
		quad.Int(math.MinInt64 + 1), quad.Int(math.MinInt64),
	}},
}

func TestCompareTypedValues(t testing.TB, gen testutil.Database) {
	conf := gen.Config
	if conf.UnTyped {
		t.SkipNow()
	}
	qs, opts, closer := gen.Run(t)
	defer closer()

	w := testutil.MakeWriter(t, qs, opts)

	t1 := tzero
	t2 := t1.Add(time.Hour)
	t3 := t2.Add(time.Hour * 48)
	t4 := t1.Add(time.Hour * 24 * 365)

	quads := []quad.Quad{
		{quad.BNode("alice"), quad.BNode("bob"), quad.BNode("charlie"), quad.BNode("dani")},
		{quad.IRI("alice"), quad.IRI("bob"), quad.IRI("charlie"), quad.IRI("dani")},
		{quad.String("alice"), quad.String("bob"), quad.String("charlie"), quad.String("dani")},
		{quad.Int(100), quad.Int(112), quad.Int(110), quad.Int(20)},
		{quad.Time(t1), quad.Time(t2), quad.Time(t3), quad.Time(t4)},
		// test precision as well
		{quad.Int(math.MaxInt64), quad.Int(math.MaxInt64 - 1), quad.Int(math.MinInt64 + 1), quad.Int(math.MinInt64)},
	}

	err := w.AddQuadSet(quads)
	require.NoError(t, err)

	var vals []quad.Value
	for _, q := range quads {
		for _, d := range quad.Directions {
			if v := q.Get(d); v != nil {
				vals = append(vals, v)
			}
		}
	}
	ExpectIteratedValues(t, qs, qs.AllNodes().BuildIterator(), vals, true)

	for _, c := range casesCompare {
		//t.Log(c.op, c.val)
		it := query.BuildIterator(qs, gshape.CompareNodes(qs.AllNodes(), c.op, c.val))
		ExpectIteratedValues(t, qs, it, c.expect, true)
	}

	for _, c := range casesCompare {
		s := gshape.CompareNodes(qs.AllNodes(), c.op, c.val)
		ns, ok := query.Optimize(s, qs)
		require.Equal(t, conf.OptimizesComparison, ok)
		if conf.OptimizesComparison {
			require.NotEqual(t, s, ns)
		} else {
			require.Equal(t, s, ns)
		}
		nit := query.BuildIterator(qs, ns)
		ExpectIteratedValues(t, qs, nit, c.expect, true)
	}
}

func TestNodeDelete(t testing.TB, gen testutil.Database) {
	qs, opts, closer := gen.Run(t)
	defer closer()

	w := testutil.MakeWriter(t, qs, opts, MakeQuadSet()...)

	del := quad.Raw("D")

	err := w.RemoveNode(del)
	require.NoError(t, err)

	exp := MakeQuadSet()
	for i := 0; i < len(exp); i++ {
		for _, d := range quad.Directions {
			if exp[i].Get(d) == del {
				exp = append(exp[:i], exp[i+1:]...)
				i--
				break
			}
		}
	}
	ExpectIteratedQuads(t, qs, qs.AllQuads().BuildIterator(), exp, true)

	ExpectIteratedValues(t, qs, qs.AllNodes().BuildIterator(), []quad.Value{
		quad.Raw("A"),
		quad.Raw("B"),
		quad.Raw("C"),
		quad.Raw("E"),
		quad.Raw("F"),
		quad.Raw("G"),
		quad.Raw("cool"),
		quad.Raw("follows"),
		quad.Raw("status"),
		quad.Raw("status_graph"),
	}, true)
}

func TestSchema(t testing.TB, gen testutil.Database) {
	qs, opts, closer := gen.Run(t)
	defer closer()

	w := testutil.MakeWriter(t, qs, opts, MakeQuadSet()...)

	type Person struct {
		_         struct{}   `quad:"@type > ex:Person"`
		ID        quad.IRI   `quad:"@id" json:"id"`
		Name      string     `quad:"ex:name" json:"name"`
		Something []quad.IRI `quad:"isParentOf < *,optional" json:"something"`
	}
	p := Person{
		ID:   quad.IRI("ex:bob"),
		Name: "Bob",
	}

	sch := schema.NewConfig()

	qw := graph.NewWriter(w)
	id, err := sch.WriteAsQuads(qw, p)
	require.NoError(t, err)
	err = qw.Close()
	require.NoError(t, err)
	require.Equal(t, p.ID, id)

	var p2 Person
	err = sch.LoadTo(nil, qs, &p2, id)
	require.NoError(t, err)
	require.Equal(t, p, p2)
}

func randString() string {
	const n = 60
	b := bytes.NewBuffer(nil)
	b.Grow(n)
	for i := 0; i < n; i++ {
		b.WriteByte(byte('a' + rand.Intn(26)))
	}
	return b.String()
}

func testConcurrent(t testing.TB, gen testutil.Database) {
	ctx := context.TODO()
	qs, opts, closer := gen.Run(t)
	defer closer()
	if opts == nil {
		opts = make(graph.Options)
	}
	opts["ignore_duplicate"] = true
	qw := testutil.MakeWriter(t, qs, opts)

	const n = 1000
	subjects := make([]string, 0, n/4)
	for i := 0; i < cap(subjects); i++ {
		subjects = append(subjects, randString())
	}
	var wg sync.WaitGroup
	wg.Add(2)
	done := make(chan struct{})
	go func() {
		defer wg.Done()
		defer close(done)
		for i := 0; i < n; i++ {
			n1 := subjects[rand.Intn(len(subjects))]
			n2 := subjects[rand.Intn(len(subjects))]
			t := graph.NewTransaction()
			t.AddQuad(quad.Make(n1, "link", n2, nil))
			t.AddQuad(quad.Make(n2, "link", n1, nil))
			if err := qw.ApplyTransaction(t); err != nil {
				panic(err)
			}
		}
	}()

	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			n1 := subjects[rand.Intn(len(subjects))]
			ref, _ := graph.RefOf(ctx, qs, quad.String(n1))
			it := qs.QuadIterator(quad.Subject, ref).BuildIterator()
			for it.Next(ctx) {
				q := qs.Quad(it.Result())
				_ = q.Subject.Native()
				_ = q.Predicate.Native()
				_ = q.Object.Native()
			}
			if err := it.Close(); err != nil {
				panic(err)
			}
		}
	}()
	wg.Wait()
}
