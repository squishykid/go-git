// Package bitmap implements encoding and decoding of pack bitmap index
// files (*.bitmap).
//
// Pack bitmap index files accelerate object reachability queries by
// storing precomputed reachability bitmaps for selected commits.
//
// == On-disk format (v1)
//
//	  === Header
//
//	  4-byte signature: {'B', 'I', 'T', 'M'}
//	  2-byte version number (network byte order)
//	  2-byte option flags (network byte order)
//	  4-byte entry count (network byte order)
//	  H-byte checksum of the corresponding packfile (H = hash length)
//
//	  === Type Bitmaps
//
//	  4 EWAH bitmaps, one for each object type in the order:
//	  commits, trees, blobs, tags. Each bitmap has one bit per
//	  object in the packfile index; a set bit indicates the object
//	  at that index position is of the corresponding type.
//
//	  === Bitmap Entries
//
//	  entry_count entries, each containing:
//	    4-byte object position in the pack index (network byte order)
//	    1-byte XOR offset
//	    1-byte flags
//	    EWAH compressed bitmap
//
//	  === Trailing Data
//
//	  If BITMAP_OPT_HASH_CACHE (0x4) is set:
//	    N 4-byte name-hash values, one per object in the pack.
//
//	  H-byte checksum of the entire bitmap file.
//
// Refer to:
// https://github.com/git/git/blob/master/Documentation/gitformat-pack.adoc
package bitmap