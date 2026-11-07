// Bounded deterministic programs; no host access, network, process spawning, or unbounded allocation.
export const programs = [
 {name:'python',language:'python',expected:'accepted',kind:'small',source:'a,b=map(int,input().split()); print(a+b)'},
 {name:'cpp',language:'cpp',expected:'accepted',kind:'representative',source:'#include <iostream>\nint main(){long long a,b;std::cin>>a>>b;std::cout<<a+b<<"\\n";}'},
 {name:'java',language:'java',expected:'accepted',kind:'small',source:'import java.util.*; public class Main {public static void main(String[] args){Scanner s=new Scanner(System.in); System.out.println(s.nextLong()+s.nextLong());}}'},
 {name:'wrong',language:'python',expected:'wrong_answer',kind:'small',source:'print(-1)'},
 {name:'compile',language:'cpp',expected:'failed_to_process',kind:'small',source:'int main( {'},
 {name:'runtime',language:'python',expected:'rte',kind:'small',source:'raise RuntimeError("synthetic")'},
 {name:'time',language:'cpp',expected:'tle',kind:'small',source:'#include <iostream>\nint main(){volatile unsigned long long s=0;for(unsigned long long i=0;i<10000000000ULL;++i)s+=i;std::cout<<s;}'},
 // The API maps sandbox memory exhaustion to rte; no distinct deployed MLE mapping exists.
 {name:'memory',language:'python',expected:'rte',kind:'memory',source:'x=bytearray(96*1024*1024); print(len(x))'},
 {name:'moderate',language:'python',expected:'accepted',kind:'representative',source:'a,b=map(int,input().split()); x=sum(i*i for i in range(100000)); print(a+b)'},
 {name:'fanout',language:'cpp',expected:'accepted',kind:'fanout',source:'#include <iostream>\nint main(){int a,b;std::cin>>a>>b;std::cout<<a+b<<"\\n";}'},
 {name:'mcq-correct',language:'',expected:'accepted',kind:'mcq',option:[0]},
 {name:'mcq-wrong',language:'',expected:'wrong_answer',kind:'mcq_wrong',option:[1]},
 {name:'output-volume',language:'python',expected:'wrong_answer',kind:'small',source:'print("x"*4095)'},
];
export function programAt(index, matrix=false) { return programs[matrix ? index%programs.length : [0,1,2,8][index%4]]; }
