interface Props {
  subjects: string[];
}

export function CompareShell({ subjects }: Props) {
  let message = "Choose a subject in the Inspector, then choose Compare.";
  if (subjects.length === 1) {
    message = `Comparing ${subjects[0]}. Search for a second subject.`;
  } else if (subjects.length === 2) {
    message = `${subjects[0]} and ${subjects[1]} are selected. Detailed comparison is not available yet.`;
  }
  return (
    <section className="compare-shell" aria-label="Compare">
      <h2>Compare</h2>
      <p>{message}</p>
    </section>
  );
}
