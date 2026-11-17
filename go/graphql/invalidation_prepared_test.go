package graphql

import "testing"

func TestPreparedTypedContainersDetachAtSharedDecoderBoundary(t *testing.T) {
	source := map[string]any{"lists": [][]int32{{1, 2}}, "bytes": []byte{3, 4}}
	copied, err := coercePrepared[map[string]any](source)
	if err != nil {
		t.Fatal(err)
	}
	copied["lists"].([][]int32)[0][0] = 99
	copied["bytes"].([]byte)[0] = 88
	if source["lists"].([][]int32)[0][0] != 1 || source["bytes"].([]byte)[0] != 3 {
		t.Fatal("assignable container bypassed detached decoding")
	}
	var nilList []int32
	nilCopy, err := coercePrepared[[]int32](nilList)
	if err != nil || nilCopy != nil {
		t.Fatalf("nil list changed: %#v %v", nilCopy, err)
	}
	emptyCopy, err := coercePrepared[[]int32]([]int32{})
	if err != nil || emptyCopy == nil || len(emptyCopy) != 0 {
		t.Fatalf("empty list changed: %#v %v", emptyCopy, err)
	}
	var nilMap map[string]int32
	mapCopy, err := coercePrepared[map[string]int32](nilMap)
	if err != nil || mapCopy != nil {
		t.Fatalf("nil map changed: %#v %v", mapCopy, err)
	}
}
