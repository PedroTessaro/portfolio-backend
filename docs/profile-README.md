<!--
  Profile README — copy everything below into PedroTessaro/PedroTessaro/README.md
  (the repo named after the user, which GitHub shows at the top of the profile).

  Check first:
    1. the Fly app is up and its URL matches the one in config.yaml
    2. open the SVG URL in a browser once, to confirm the animation runs
-->

<picture>
  <source media="(prefers-color-scheme: light)" srcset="https://pedrotessaro.fly.dev/terminal.svg?theme=light">
  <img alt="Terminal showing: Pedro Tessaro, Backend Engineer. Go, Java, C/C++, PostgreSQL, Docker." src="https://pedrotessaro.fly.dev/terminal.svg">
</picture>

### That terminal is not a GIF

It is a Go service rendering an SVG on every request, with live numbers from the
GitHub API, a SQLite view counter and its own runtime metrics. The `curl` command
it types works — try it:

```console
$ curl -s https://pedrotessaro.fly.dev/whoami
```

Source, and why animating a README is harder than it looks:
**[portfolio-backend](https://github.com/PedroTessaro/portfolio-backend)**

---

### Backend & systems

I build services and I like the layers underneath them — how memory is laid out,
how a scheduler decides, how a parser turns text into structure. Most of what I
write ends up being about making those layers explicit.

| | |
|---|---|
| **[portfolio-backend](https://github.com/PedroTessaro/portfolio-backend)** | The service behind the terminal above. Go, SMIL, SQLite, Fly.io |
| **[RSSAggregator](https://github.com/PedroTessaro/RSSAggregator)** | Feed aggregator in Go — concurrent fetching, persistence, REST API |
| **[AssemblerImplementation](https://github.com/PedroTessaro/AssemblerImplementation)** | A two-pass assembler in Java: symbol table, relocation, object output |
| **[ReversePolishNotationInterpreter](https://github.com/PedroTessaro/ReversePolishNotationInterpreter)** | Stack-based expression interpreter in Java |
| **[parallel_programming_studies](https://github.com/PedroTessaro/parallel_programming_studies)** | Concurrency primitives in C: threads, synchronization, shared memory |
| **[TextEditor](https://github.com/PedroTessaro/TextEditor)** | Terminal text editor in C++, built from raw-mode input up |

Currently going deeper into Go, distributed systems and everything that happens
between a request arriving and a row being written.

---

### Elsewhere

Computer Science student, piano teacher, and — before backend took over — a fair
number of Swift apps built at the **Apple Developer Academy | Mackenzie**. They
are still in [my repositories](https://github.com/PedroTessaro?tab=repositories);
they just are not what I am building now.

[LinkedIn](https://www.linkedin.com/in/pedrotessaro/) ·
[ptssar22@gmail.com](mailto:ptssar22@gmail.com)
