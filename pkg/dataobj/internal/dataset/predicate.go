package dataset

import (
	"fmt"
	"iter"
	"unsafe"
)

// Predicate is an expression used to filter rows in a [Reader].
type Predicate interface{ isPredicate() }

// Suppported predicates..
type (
	// An AndPredicate is a [Predicate] which asserts that a row may only be
	// included if both the Left and Right Predicate are true.
	AndPredicate struct{ Left, Right Predicate }

	// An OrPredicate is a [Predicate] which asserts that a row may only be
	// included if either the Left or Right Predicate are true.
	OrPredicate struct{ Left, Right Predicate }

	// A NotePredicate is a [Predicate] which asserts that a row may only be
	// included if the inner Predicate is false.
	NotPredicate struct{ Inner Predicate }

	// TruePredicate is a [Predicate] which always returns true.
	TruePredicate struct{}

	// FalsePredicate is a [Predicate] which always returns false.
	FalsePredicate struct{}

	// An EqualPredicate is a [Predicate] which asserts that a row may only be
	// included if the Value of the Column is equal to the Value.
	EqualPredicate struct {
		Column Column // Column to check.
		Value  Value  // Value to check equality for.
	}

	// An InPredicate is a [Predicate] which asserts that a row may only be
	// included if the Value of the Column is present in the provided Values.
	InPredicate struct {
		Column Column   // Column to check.
		Values ValueSet // Set of values to check.
	}

	// A GreaterThanPredicate is a [Predicate] which asserts that a row may only
	// be included if the Value of the Column is greater than the provided Value.
	GreaterThanPredicate struct {
		Column Column // Column to check.
		Value  Value  // Value for which rows in Column must be greater than.
	}

	// A LessThanPredicate is a [Predicate] which asserts that a row may only be
	// included if the Value of the Column is less than the provided Value.
	LessThanPredicate struct {
		Column Column // Column to check.
		Value  Value  // Value for which rows in Column must be less than.
	}

	// FuncPredicate is a [Predicate] which asserts that a row may only be
	// included if the Value of the Column passes the Keep function.
	//
	// Instances of FuncPredicate are ineligible for page filtering and should
	// only be used when there isn't a more explicit Predicate implementation.
	FuncPredicate struct {
		Column Column // Column to check.

		// Keep is invoked with the column and value pair to check. Keep is given
		// the Column instance to allow for reusing the same function across
		// multiple columns, if necessary.
		//
		// If Keep returns true, the row is kept.
		Keep func(column Column, value Value) bool
	}
)

func (AndPredicate) isPredicate()         {}
func (OrPredicate) isPredicate()          {}
func (NotPredicate) isPredicate()         {}
func (TruePredicate) isPredicate()        {}
func (FalsePredicate) isPredicate()       {}
func (EqualPredicate) isPredicate()       {}
func (InPredicate) isPredicate()          {}
func (GreaterThanPredicate) isPredicate() {}
func (LessThanPredicate) isPredicate()    {}
func (FuncPredicate) isPredicate()        {}

// WalkPredicate traverses a predicate in depth-first order: it starts by
// calling fn(p). If fn(p) returns true, WalkPredicate is invoked recursively
// with fn for each of the non-nil children of p, followed by a call of
// fn(nil).
func WalkPredicate(p Predicate, fn func(p Predicate) bool) {
	if p == nil || !fn(p) {
		return
	}

	switch p := p.(type) {
	case AndPredicate:
		WalkPredicate(p.Left, fn)
		WalkPredicate(p.Right, fn)

	case OrPredicate:
		WalkPredicate(p.Left, fn)
		WalkPredicate(p.Right, fn)

	case NotPredicate:
		WalkPredicate(p.Inner, fn)

	case TruePredicate: // No children.
	case FalsePredicate: // No children.
	case EqualPredicate: // No children.
	case InPredicate: // No children.
	case GreaterThanPredicate: // No children.
	case LessThanPredicate: // No children.
	case FuncPredicate: // No children.

	default:
		panic(fmt.Sprintf("dataset.WalkPredicate: unsupported predicate type %T", p))
	}

	fn(nil)
}

type ValueSet interface {
	Contains(value Value) bool
	Iter() iter.Seq[Value]
	Size() int
}

type Int64Set struct {
	values map[int64]Value
}

func NewInt64ValueSet(values []Value) Int64Set {
	valuesMap := make(map[int64]Value, len(values))
	for _, v := range values {
		valuesMap[v.Int64()] = v
	}
	return Int64Set{
		values: valuesMap,
	}
}

func (s Int64Set) Contains(value Value) bool {
	_, ok := s.values[value.Int64()]
	return ok
}

func (s Int64Set) Iter() iter.Seq[Value] {
	return func(yield func(v Value) bool) {
		for _, v := range s.values {
			ok := yield(v)
			if !ok {
				return
			}
		}
	}
}

func (s Int64Set) Size() int {
	return len(s.values)
}

type Uint64ValueSet struct {
	values map[uint64]Value
}

func NewUint64ValueSet(values []Value) Uint64ValueSet {
	valuesMap := make(map[uint64]Value, len(values))
	for _, v := range values {
		valuesMap[v.Uint64()] = v
	}
	return Uint64ValueSet{
		values: valuesMap,
	}
}

func (s Uint64ValueSet) Contains(value Value) bool {
	_, ok := s.values[value.Uint64()]
	return ok
}

func (s Uint64ValueSet) Iter() iter.Seq[Value] {
	return func(yield func(v Value) bool) {
		for _, v := range s.values {
			ok := yield(v)
			if !ok {
				return
			}
		}
	}
}

func (s Uint64ValueSet) Size() int {
	return len(s.values)
}

type ByteArrayValueSet struct {
	values map[string]Value
}

func NewByteArrayValueSet(values []Value) ByteArrayValueSet {
	valuesMap := make(map[string]Value, len(values))
	for _, v := range values {
		valuesMap[unsafeString(v.ByteArray())] = v
	}
	return ByteArrayValueSet{
		values: valuesMap,
	}
}

func (s ByteArrayValueSet) Contains(value Value) bool {
	_, ok := s.values[unsafeString(value.ByteArray())]
	return ok
}

func (s ByteArrayValueSet) Iter() iter.Seq[Value] {
	return func(yield func(v Value) bool) {
		for _, v := range s.values {
			ok := yield(v)
			if !ok {
				return
			}
		}
	}
}

func (s ByteArrayValueSet) Size() int {
	return len(s.values)
}

func unsafeString(in []byte) string {
	return unsafe.String(unsafe.SliceData(in), len(in))
}
