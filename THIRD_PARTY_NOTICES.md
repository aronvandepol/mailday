# Third-party notices

Mailday adapts code from [basecamp/hey-cli](https://github.com/basecamp/hey-cli), HEY's terminal client, under the MIT license below. Mailday is not affiliated with 37signals or HEY.

- `internal/tui`: navigation, the mail list, the week calendar, the Omarchy theme loading and the help bar, from `internal/tui`
- `internal/htmlutil` and `internal/markdown`: HTML to Markdown conversion and terminal rendering of mail, from the same packages in hey-cli
- `internal/terminal`: control-sequence sanitising, from hey-cli's `sanitize.go`

Source

- https://github.com/basecamp/hey-cli
- Commit used for the first adaptation `58c83f1`. The reader packages and the sanitiser were ported later from hey-cli's main branch (October 2026)

License

MIT License

Copyright (c) 2026 37signals, LLC

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
