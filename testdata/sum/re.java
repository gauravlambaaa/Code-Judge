// Divides by zero: ArithmeticException, exit code 1. Expect RE
public class Main {
    public static void main(String[] args) {
        System.out.println(5 / Integer.parseInt("0"));
    }
}
