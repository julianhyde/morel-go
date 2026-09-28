(*
 * Licensed to Julian Hyde under one or more contributor license
 * agreements.  See the NOTICE file distributed with this work
 * for additional information regarding copyright ownership.
 * Julian Hyde licenses this file to you under the Apache
 * License, Version 2.0 (the "License"); you may not use this
 * file except in compliance with the License.  You may obtain a
 * copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing,
 * software distributed under the License is distributed on an
 * "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
 * either express or implied.  See the License for the specific
 * language governing permissions and limitations under the
 * License.
 *
 * The "scott" dataset, a global relation available to every
 * script, mirroring morel-java's foreign "scott" database.
 *)
val scott = {
  bonuses = bag [] : {comm:decimal, ename:string, job:string, sal:decimal} bag,
  emps = bag [
    {comm = NONE, deptno = 20, empno = 7369, ename = "SMITH", hiredate = "1980-12-17", job = "CLERK", mgr = SOME 7902, sal = decimal "800"},
    {comm = SOME (decimal "300"), deptno = 30, empno = 7499, ename = "ALLEN", hiredate = "1981-02-20", job = "SALESMAN", mgr = SOME 7698, sal = decimal "1600"},
    {comm = SOME (decimal "500"), deptno = 30, empno = 7521, ename = "WARD", hiredate = "1981-02-22", job = "SALESMAN", mgr = SOME 7698, sal = decimal "1250"},
    {comm = NONE, deptno = 20, empno = 7566, ename = "JONES", hiredate = "1981-02-04", job = "MANAGER", mgr = SOME 7839, sal = decimal "2975"},
    {comm = SOME (decimal "1400"), deptno = 30, empno = 7654, ename = "MARTIN", hiredate = "1981-09-28", job = "SALESMAN", mgr = SOME 7698, sal = decimal "1250"},
    {comm = NONE, deptno = 30, empno = 7698, ename = "BLAKE", hiredate = "1981-01-05", job = "MANAGER", mgr = SOME 7839, sal = decimal "2850"},
    {comm = NONE, deptno = 10, empno = 7782, ename = "CLARK", hiredate = "1981-06-09", job = "MANAGER", mgr = SOME 7839, sal = decimal "2450"},
    {comm = NONE, deptno = 20, empno = 7788, ename = "SCOTT", hiredate = "1987-04-19", job = "ANALYST", mgr = SOME 7566, sal = decimal "3000"},
    {comm = NONE, deptno = 10, empno = 7839, ename = "KING", hiredate = "1981-11-17", job = "PRESIDENT", mgr = NONE, sal = decimal "5000"},
    {comm = SOME (decimal "0"), deptno = 30, empno = 7844, ename = "TURNER", hiredate = "1981-09-08", job = "SALESMAN", mgr = SOME 7698, sal = decimal "1500"},
    {comm = NONE, deptno = 20, empno = 7876, ename = "ADAMS", hiredate = "1987-05-23", job = "CLERK", mgr = SOME 7788, sal = decimal "1100"},
    {comm = NONE, deptno = 30, empno = 7900, ename = "JAMES", hiredate = "1981-12-03", job = "CLERK", mgr = SOME 7698, sal = decimal "950"},
    {comm = NONE, deptno = 20, empno = 7902, ename = "FORD", hiredate = "1981-12-03", job = "ANALYST", mgr = SOME 7566, sal = decimal "3000"},
    {comm = NONE, deptno = 10, empno = 7934, ename = "MILLER", hiredate = "1982-01-23", job = "CLERK", mgr = SOME 7782, sal = decimal "1300"}],
  depts = bag [
    {deptno = 10, dname = "ACCOUNTING", loc = "NEW YORK"},
    {deptno = 20, dname = "RESEARCH", loc = "DALLAS"},
    {deptno = 30, dname = "SALES", loc = "CHICAGO"},
    {deptno = 40, dname = "OPERATIONS", loc = "BOSTON"}],
  salgrades = bag [
    {grade = 1, hisal = decimal "1200", losal = decimal "700"},
    {grade = 2, hisal = decimal "1400", losal = decimal "1201"},
    {grade = 3, hisal = decimal "2000", losal = decimal "1401"},
    {grade = 4, hisal = decimal "3000", losal = decimal "2001"},
    {grade = 5, hisal = decimal "9999", losal = decimal "3001"}]
};
