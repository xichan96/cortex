// Package sets provides a set of functions for working with sets
package sets

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Set is a set of elements
// T must be comparable, as map key, val uses empty struct to reduce space
type Set[T comparable] map[T]struct{}

// NewSet creates a new set from a list of elements
func NewSet[T comparable](es ...T) Set[T] {
	s := Set[T]{}
	s.Add(es...)
	return s
}

func NewSetBySlice[T comparable, D any](slice []D, fn func(d D) T) Set[T] {
	s := Set[T]{}
	for _, d := range slice {
		s.Add(fn(d))
	}
	return s
}

func (s Set[T]) String() string {
	bs := strings.Builder{}
	bs.WriteByte('(')
	i := 0
	for k := range s {
		bs.WriteString(fmt.Sprintf("%v", k))
		if i != len(s)-1 {
			bs.WriteString(",")
		}
		i++
	}
	bs.WriteByte(')')
	return bs.String()
}

// Len report the elements number of s
func (s Set[T]) Len() int {
	return len(s)
}

// IsEmpty report weather s is empty
func (s Set[T]) IsEmpty() bool {
	return s.Len() == 0
}

// Add elements to set s
// if element is already in s this has no effect
func (s Set[T]) Add(es ...T) {
	for _, e := range es {
		s[e] = struct{}{}
	}
}

// Remove  elements from set s
// if element is not in s this has no effect
func (s Set[T]) Remove(es ...T) {
	for _, e := range es {
		delete(s, e)
	}
}

// Contains report wether v is in s
func (s Set[T]) Has(v T) bool {
	_, ok := s[v]
	return ok
}

// Contains report wether v is in s
func (s Set[T]) Contains(v T) bool {
	return s.Has(v)
}

// Clone create a new set with the same elements as s
func (s Set[T]) Clone() Set[T] {
	r := Set[T]{}
	r.Add(s.Slice()...)
	return r
}

// Slice transform set to slice
func (s Set[T]) Slice() []T {
	r := make([]T, 0, s.Len())

	for e := range s {
		r = append(r, e)
	}

	return r
}

// Union returns the union of two sets
func (s Set[T]) Union(t Set[T]) Set[T] {
	r := NewSet[T](s.Slice()...)
	for key := range t {
		r.Add(key)
	}
	return r
}

// Intersection returns the intersection of two sets
func (s Set[T]) Intersection(t Set[T]) Set[T] {
	r := Set[T]{}
	for key := range s {
		if t.Contains(key) {
			r.Add(key)
		}
	}
	return r
}

// Difference returns the difference of two sets
func (s Set[T]) Difference(t Set[T]) Set[T] {
	r := Set[T]{}
	for key := range s {
		if !t.Contains(key) {
			r.Add(key)
		}
	}
	return r
}

// Complement returns the complement of two sets
func (s Set[T]) Complement(t Set[T]) Set[T] {
	r := Set[T]{}
	for key := range t {
		if !s.Contains(key) {
			r.Add(key)
		}
	}
	return r
}

// UnmarshalJSON ...
func (s *Set[T]) UnmarshalJSON(bytes []byte) error {
	var bs []T
	if err := json.Unmarshal(bytes, &bs); err != nil {
		return err
	}
	*s = NewSet(bs...)
	return nil

}

// MarshalJSON ...
func (s Set[T]) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.Slice())
}
