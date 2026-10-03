# hello — example Talon plugin

Demonstrates the plugin protocol end to end:

```bash
cd examples/plugins/hello
go build -o hello .
talon plugins test .
talon plugins install .
```

Then, inside a Talon session:

```
/tools
> greet Luca
```

The plugin exposes `hello_greet` and `hello_now`, both classified as read-only
tools so they run without confirmation.
