// Keeps allocating 10 MB arrays until the JVM throws OutOfMemoryError.
// Expect MLE
import java.util.ArrayList;
import java.util.List;

public class Main {
    public static void main(String[] args) {
        List<byte[]> blocks = new ArrayList<>();
        while (true) blocks.add(new byte[10 << 20]);
    }
}
