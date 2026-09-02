# User Media

> English | [中文](README.zh.md)

`usermedia` owns validation and Thread scoping for image Inputs. It validates
bounded image data, stores content-addressed bytes through `internal/artifact`,
and verifies that references belong to the target Thread before admission.

Local relative paths resolve from the Workspace. Prepared batches are
validated completely before persistence. HTTP multipart parsing, CLI behavior,
message Blocks, and Provider encoding belong to their transport and runtime
owners.
