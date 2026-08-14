package sandbox

type hardLinkIdentity struct {
	device uint64
	inode  uint64
}

type hardLinkMetadata struct {
	identity hardLinkIdentity
	links    uint64
}
