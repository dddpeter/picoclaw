import { Fragment, memo, useMemo } from "react"

import { parseAnsiSegments, wrapLogLine } from "@/lib/ansi-log"

type AnsiLogLineProps = {
  line: string
  wrapColumns: number
}

// Memoized so appending new lines never re-renders existing ones (props keep
// their string references across `[...prev, ...next]` appends).
// content-visibility lets the browser skip painting offscreen lines entirely,
// which keeps long log buffers from flashing on every poll.
export const AnsiLogLine = memo(function AnsiLogLine({
  line,
  wrapColumns,
}: AnsiLogLineProps) {
  const segments = useMemo(() => {
    return parseAnsiSegments(wrapLogLine(line, wrapColumns))
  }, [line, wrapColumns])

  return (
    <div className="break-normal whitespace-pre-wrap [content-visibility:auto] [contain-intrinsic-size:auto_1.625em]">
      {segments.map((segment, index) => (
        <Fragment key={`${index}-${segment.text.length}`}>
          <span style={segment.style}>{segment.text}</span>
        </Fragment>
      ))}
    </div>
  )
})
