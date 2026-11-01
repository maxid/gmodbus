# interop

Interoperability tests against an independent Modbus implementation
([`simonvetter/modbus`](https://github.com/simonvetter/modbus)).

The tests live in their own Go module so the library's module graph does not
carry a third-party Modbus dependency. The module replaces
`github.com/maxid/gmodbus` with the parent directory, so it always exercises the
working copy and is meant to be run from a checkout, never published on its own:

```sh
cd interop && go test ./...
```

CI runs this module separately. When the parent module changes its dependency
pins, refresh the sums here:

```sh
cd interop && go mod tidy
```
