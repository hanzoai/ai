// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import "testing"

// A store that has changed embedding model holds vectors of two dimensions. One
// from a different space is not a near neighbour and not a far one either — it is
// not comparable — so the search leaves it out and answers with the rest.
func TestASearchSkipsVectorsFromAnotherSpace(t *testing.T) {
	target := []float32{1, 0, 0}
	vectors := [][]float32{
		{1, 0, 0},          // the same vector
		{1, 0, 0, 0, 0, 0}, // a different model's output
		{0, 1, 0},          // orthogonal
	}
	got, err := getNearestVectors(target, vectors, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ranked %d vectors, want the 2 that are comparable", len(got))
	}
	if got[0].Index != 0 {
		t.Errorf("the identical vector ranked %d, not first", got[0].Index)
	}
	for _, s := range got {
		if s.Index == 1 {
			t.Error("a vector from another space was ranked")
		}
	}
}

// And the measure itself is total, so a caller that has not filtered first gets
// an answer rather than a panic.
func TestComparingVectorsOfDifferentLengths(t *testing.T) {
	a, b := []float32{1, 0, 0}, []float32{1, 0, 0, 0}
	if got := cosineSimilarity(a, b, norm(a)); got != 0 {
		t.Errorf("cosineSimilarity across dimensions = %v, want 0", got)
	}
	if got := dot(a, b); got != 0 {
		t.Errorf("dot across dimensions = %v, want 0", got)
	}
}
