# NAME

slinit-shell-var - sanitise strings into shell-variable names
(OpenRC-compatible)

# SYNOPSIS

**slinit-shell-var** *STRING*...

# DESCRIPTION

**slinit-shell-var** is a drop-in replacement for OpenRC's
**shell_var**(1). It reads its arguments, replaces every non-
alphanumeric byte with **_**, joins the results with a single space,
and writes the whole line to stdout.

The tool exists so that ported OpenRC init.d scripts and helper
functions can build shell-variable identifiers out of user-facing
identifiers that would otherwise contain punctuation:

```
svcname=$(slinit-shell-var "$RC_SVCNAME")   # my-thing.d/1  → my_thing_d_1
eval "${svcname}_PORT=8080"
```

# BEHAVIOUR

- Each byte of each argument is inspected in turn. A byte is kept
  verbatim iff it matches **[0-9A-Za-z]**. Anything else — dashes,
  dots, slashes, existing underscores, ASCII controls, high-bit
  bytes — is replaced with **_**.
- Arguments are joined with a literal space, matching the C
  original. Spaces **inside** an argument still become **_**.
- With no arguments, output is a single newline.

# EXIT STATUS

Always **0**.

# EXAMPLES

```
$ slinit-shell-var my-service.d/config
my_service_d_config

$ slinit-shell-var a.b c-d
a_b c_d
```

# SEE ALSO

**shell_var**(1) (OpenRC), **slinit-einfo**(8).
