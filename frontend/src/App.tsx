import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";

function App() {
  return (
    <main className="p-8 font-sans">
      <h1 className="text-2xl font-semibold">juex web viewer</h1>
      <div className="mt-4 flex items-center gap-3">
        <Button>Send</Button>
        <Button variant="outline">Stop</Button>
        <Badge variant="secondary">scaffold ready</Badge>
      </div>
    </main>
  );
}

export default App;
